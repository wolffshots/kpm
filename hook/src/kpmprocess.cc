// NICKEL-UI.md §4 — KpmProcess implementation, adapted from NickelHardcover's
// cli.cc (MIT). The Wi-Fi bring-up (constructor + connectingFailed + the
// singleShot-yield) is copied near-verbatim; the output protocol matches
// kpm 0.6.0's --json mode (JSON-OUTPUT.md §1: human log lines, then a final
// BEGIN_JSON{...} line).

#include <QJsonArray>
#include <QJsonDocument>
#include <QProcess>
#include <QTimer>

#include <NickelHook.h>

#include "files.h"
#include "kpmprocess.h"
#include "nkpm.h"

bool KpmProcess::s_mutatingInFlight = false;

// Watchdog windows (§4): mutating ops may download on slow Wi-Fi (5 min); reads
// are local and quick.
static const int kMutatingWatchdogMs = 5 * 60 * 1000;
static const int kReadWatchdogMs = 60 * 1000;
static const int kWifiTimeoutMs = 30 * 1000;

// Hard cap on the stdout one kpm run may produce. The largest legitimate payload
// is `search --json`, and it is the one that grows: each package object runs
// roughly 400-1000 bytes once description/homepage/source/tested_fw/conflicts are
// present, so today's 12-package registry is ~12 KB. The worst realistic outlier
// is a single package whose whole manifest failed to land, listing thousands of
// `missing_files` paths at ~64 bytes each (a few hundred KB). 4 MiB covers a
// registry two orders of magnitude larger than today's *plus* that outlier, while
// staying a rounding error against Nickel's own footprint.
//
// Past the cap the output is a runaway (a loop printing), not a payload, so the
// run is killed and reported. It is deliberately never truncated-and-parsed: that
// is the same rule as the missing-BEGIN_JSON branch in processFinished, and for
// the same reason — a clipped JSON body must surface as an error, never render as
// an empty success.
static const int kMaxStdoutBytes = 4 * 1024 * 1024;

// Cap on retained stderr. Deliberately far smaller than the stdout cap, because
// stderr is never a payload — the JSON always rides stdout after BEGIN_JSON — so
// nothing downstream parses it and nothing needs it whole. It has exactly two
// consumers: the "another kpm instance" substring check (kpm emits the lock message
// on its first line) and the error-dialog text, itself bounded to kMaxDialogChars.
// The worst realistic legitimate stderr is every package of a 12-entry registry
// failing one update at ~150 bytes each, i.e. ~2 KB; 8 KiB is several times that and
// still covers a ~50-package all-fail run. nh_log truncates at 256 bytes regardless
// (NickelHook/nh.c:249 — a 256-byte thread-local buffer through vsnprintf), so no
// consumer can use more than a few hundred bytes anyway.
//
// Unlike the stdout cap this does NOT end the run. stderr is not protected by the
// BEGIN_JSON rule, so a clipped tail can never be mistaken for a valid result, and a
// perfectly good stdout payload must not be discarded because the process was chatty
// on the other channel. Excess is read off the pipe (so kpm never blocks writing)
// and then dropped, keeping the informative head.
static const int kMaxStderrBytes = 8 * 1024;

// Cap on the text handed to a Nickel error dialog. The dialog has no scrollbar: at
// the Small text size these devices use, roughly 40 characters fit per line across
// its width, so ~400 characters is about ten lines — a full dialog. Anything past
// that is simply unreachable on screen, so eliding with "…" is more honest than
// clipping invisibly.
static const int kMaxDialogChars = 400;

// ---- factories ----------------------------------------------------------

KpmProcess *KpmProcess::search() { return start({"search", "--json"}, false, false); }
// check is read-only in spirit but takes kpm's single-instance lock (it writes
// state.json), so it must ride the mutating guard too — otherwise a check
// overlapping a mutation just bounces off the lock with a "busy" error.
KpmProcess *KpmProcess::check() { return start({"check", "--json"}, true, true); }
KpmProcess *KpmProcess::registryRefresh() { return start({"registry", "refresh", "--json"}, true, true); }
KpmProcess *KpmProcess::install(const QString &id) { return start({"install", id, "--yes", "--json"}, true, true); }
// NICKEL-UI.md §6: `kpm update` is already non-interactive and has no --yes flag,
// so we pass only --json (passing --yes would be an "unknown flag" error).
KpmProcess *KpmProcess::update(const QString &id) { return start({"update", id, "--json"}, true, true); }
KpmProcess *KpmProcess::updateAll() { return start({"update", "--all", "--json"}, true, true); }
KpmProcess *KpmProcess::uninstall(const QString &id) { return start({"uninstall", id, "--yes", "--json"}, false, true); }
// list enumerates the packages that are REGISTERED in packages.d (JSON-OUTPUT.md
// §2.4) — which search cannot answer, since its rows are the union of the registry
// and the local snapshot. The one caller is DetailDialog's orphaned-def probe
// (INTEGRITY.md §4.3): registered-but-not-installed is exactly the state a failed
// two-step install leaves behind. Read-only, offline, no lock.
KpmProcess *KpmProcess::list() { return start({"list", "--json"}, false, false); }
// remove unregisters a package WITHOUT deleting any files (cmd_manage.go cmdRemove)
// — the way out of an orphaned def (INTEGRITY.md §4.3, "the complementary
// hook-side fix"). It is the one command the hook issues that has no --json mode,
// hence expectsPayload=false: exit 0 with no BEGIN_JSON is its success. It is
// mutating (it takes kpm's single-instance lock) and needs no network.
KpmProcess *KpmProcess::remove(const QString &id) { return start({"remove", id}, false, true, false); }
// sync re-copies registry defs into packages.d, propagating new [[configs]]/
// uninstall declarations to existing installs. It is mutating (takes kpm's
// single-instance lock) but needs no network — it reads only the local registry
// cache and rewrites packages.d (JSON-OUTPUT.md §2.3), so needsNetwork is false.
KpmProcess *KpmProcess::sync() { return start({"sync", "--json"}, false, true); }
// adopt-self enrols kpm's own self-update from a configured registry — the one UI
// call the "Enable self-update" button drives (kpm-self-enrol-plan §2/§5). It is
// mutating (takes kpm's lock, writes state.json) and network (best-effort refresh
// of the registry cache), so it rides s_mutatingInFlight and the Wi-Fi gate. It
// stages nothing: its payload reports staged=false/reboot_required=false.
KpmProcess *KpmProcess::adoptSelf() { return start({"adopt-self", "--json"}, true, true); }

// Config editing (CONFIG.md §3.2). list/show are offline reads; set is mutating
// (takes kpm's single-instance lock) but needs no network — a config edit is a
// local file write, so needsNetwork is false for every variant.
KpmProcess *KpmProcess::configList(const QString &id) { return start({"config", "list", id, "--json"}, false, false); }
KpmProcess *KpmProcess::configShow(const QString &id, const QString &selector) {
  return start({"config", "show", id, selector, "--json"}, false, false);
}
// configInit creates a missing file from its declared template. Mutating (takes
// kpm's single-instance lock), no network — a local file write (CONFIG.md §3.x).
KpmProcess *KpmProcess::configInit(const QString &id, const QString &selector) {
  return start({"config", "init", id, selector, "--json"}, false, true);
}
KpmProcess *KpmProcess::configSetKey(const QString &id, const QString &selector, const QString &section,
                                     const QString &key, const QString &value) {
  QStringList args{"config", "set", id, selector, "--key", key};
  if (!section.isEmpty()) {
    args << "--section" << section;
  }
  args << "--value" << value << "--json";
  return start(args, false, true);
}
KpmProcess *KpmProcess::configSetLine(const QString &id, const QString &selector, int line, const QString &value) {
  return start({"config", "set", id, selector, "--line", QString::number(line), "--value", value, "--json"}, false, true);
}
KpmProcess *KpmProcess::configAppendLine(const QString &id, const QString &selector, const QString &value) {
  return start({"config", "set", id, selector, "--append", "--value", value, "--json"}, false, true);
}
KpmProcess *KpmProcess::configDeleteLine(const QString &id, const QString &selector, int line) {
  return start({"config", "set", id, selector, "--line", QString::number(line), "--delete", "--json"}, false, true);
}

bool KpmProcess::busy() { return s_mutatingInFlight; }

KpmProcess *KpmProcess::start(QStringList arguments, bool needsNetwork, bool mutating, bool expectsPayload) {
  if (mutating && s_mutatingInFlight) {
    nh_log("KpmProcess: refusing to start a second mutating command");
    return nullptr;
  }
  return new KpmProcess(arguments, needsNetwork, mutating, expectsPayload);
}

// ---- lifecycle + Wi-Fi gate (copied from cli.cc) ------------------------

KpmProcess::KpmProcess(QStringList arguments, bool needsNetwork, bool mutating, bool expectsPayload, QObject *parent)
    : QObject(parent), arguments(arguments), needsNetwork(needsNetwork), mutating(mutating),
      expectsPayload(expectsPayload) {
  if (mutating) {
    s_mutatingInFlight = true;
  }

  WirelessWorkflowManager *wfm = WirelessWorkflowManager__sharedInstance();

  if (!needsNetwork || WirelessWorkflowManager__isInternetAccessible(wfm)) {
    networkConnected();
  } else {
    QObject::connect(wfm, SIGNAL(connectingFailed()), this, SLOT(connectingFailed()));

    showIcon(Files::wifi);

    timer = new QTimer(this);
    connect(timer, &QTimer::timeout, this, &KpmProcess::connectingFailed);
    timer->setSingleShot(true);
    timer->start(kWifiTimeoutMs);

    WirelessManager *wm = WirelessManager__sharedInstance();
    QObject::connect(wm, SIGNAL(networkConnected()), this, SLOT(networkConnected()));

    // Yield to the caller so signals are set up before a possible synchronous
    // connectingFailed() fires (the cli.cc singleShot-yield trick, §4).
    QTimer::singleShot(0, this, [] {
      WirelessWorkflowManager *wfm = WirelessWorkflowManager__sharedInstance();
      WirelessWorkflowManager__connectWireless(wfm, false, false);
    });
  }
}

KpmProcess::~KpmProcess() {
  if (icon != nullptr) {
    icon->deleteLater();
  }
}

// finish clears the mutating guard and schedules destruction. Idempotent: guard
// is only ever cleared for the process that set it.
void KpmProcess::finish() {
  if (mutating) {
    s_mutatingInFlight = false;
  }
  deleteLater();
}

// disconnectWifiSignals tears down the two sibling connections made in the
// constructor. Whichever transition wins (connected or failed) must sever BOTH,
// or a later Wi-Fi state change re-enters this object mid-run: a second
// networkConnected() would spawn a second QProcess, and a stray
// connectingFailed() would kill a running mutation and release the guard early.
void KpmProcess::disconnectWifiSignals() {
  QObject::disconnect(WirelessWorkflowManager__sharedInstance(), nullptr, this, nullptr);
  QObject::disconnect(WirelessManager__sharedInstance(), nullptr, this, nullptr);
}

void KpmProcess::connectingFailed() {
  if (done || process != nullptr) {
    return; // already running or already terminal — a Wi-Fi flap must not abort the run
  }
  done = true;
  nh_log("KpmProcess::connectingFailed()");

  disconnectWifiSignals();
  if (timer != nullptr) {
    timer->stop();
    timer->deleteLater();
    timer = nullptr;
  }

  ConfirmationDialogFactory__showErrorDialog("kpm", "Failed to connect to Wi-Fi.");
  failure("network");
  finish();
}

void KpmProcess::showIcon(const char *path) {
  if (icon == nullptr) {
    MainWindowController *mwc = MainWindowController__sharedInstance();
    QWidget *cv = MainWindowController__currentView(mwc);
    if (cv == nullptr) {
      return; // no active view (shouldn't happen mid-dialog) — skip the icon, never deref null
    }
    QWidget *window = cv->window();
    icon = new QLabel(window);
    icon->resize(90, 90);
    icon->move(window->width() - 144, window->height() - 144);
  }

  icon->setPixmap(QPixmap(path));
  icon->show();
}

void KpmProcess::networkConnected() {
  if (done || process != nullptr) {
    return; // re-entry guard: a second Wi-Fi "connected" must not spawn a second process
  }
  nh_log("KpmProcess::networkConnected()");

  disconnectWifiSignals();
  if (timer != nullptr) {
    timer->stop();
    timer->deleteLater();
    timer = nullptr;
  }

  process = new QProcess(this);
  QObject::connect(process, QOverload<int, QProcess::ExitStatus>::of(&QProcess::finished), this,
                   &KpmProcess::processFinished);
  // Drain stdout as it streams so kMaxStdoutBytes is enforced while kpm is still
  // running (QProcess otherwise buffers everything until the first read).
  QObject::connect(process, &QProcess::readyReadStandardOutput, this, &KpmProcess::readStdout);
  // Same treatment for stderr, with a much smaller cap and no kill — see
  // kMaxStderrBytes.
  QObject::connect(process, &QProcess::readyReadStandardError, this, &KpmProcess::readStderr);
  // Qt 5.2.1 has no errorOccurred; error(ProcessError) fires on exec failure
  // (FailedToStart never emits finished — without this the watchdog is the only
  // way out and the mutating guard stays stuck for its full window).
  QObject::connect(process, static_cast<void (QProcess::*)(QProcess::ProcessError)>(&QProcess::error), this,
                   &KpmProcess::processErrored);

  watchdog = new QTimer(this);
  watchdog->setSingleShot(true);
  connect(watchdog, &QTimer::timeout, this, &KpmProcess::watchdogExpired);
  watchdog->start(mutating ? kMutatingWatchdogMs : kReadWatchdogMs);

  process->start(Files::kpm, arguments);
}

// detachProcess ends a run by DETACHING the child rather than destroying it. It
// replaces the disconnect+kill pair the kill paths used to run, for two reasons
// that both end at Nickel rather than at kpm:
//
//  1. QProcess is a CHILD of KpmProcess, so finish()'s deleteLater would run
//     ~QProcess, and ~QProcess is kill() + waitForFinished() with a 30-SECOND
//     default — on Nickel's UI thread. After a SIGKILL that returns in about a
//     millisecond, but a child wedged in uninterruptible I/O freezes the whole UI
//     for the full window, and a stalled SD read is a real Kobo failure mode for a
//     process as SD-heavy as kpm. Reparenting to null takes the process out of
//     that destructor entirely: it reaps itself when the kill lands (its own
//     finished/error → its own deleteLater), and a child that never dies leaks one
//     QProcess instead of hanging the device.
//  2. Nothing may reach us afterwards. disconnect(this) drops the finished()/
//     error() the kill is about to produce, which would otherwise re-enter and
//     report a killed run as a result — the same reason the old code disconnected.
//
// A process that is already NotRunning (exec failure, or it exited a moment
// before) is deleted straight away: its destructor has nothing to wait for, and
// finished() will never fire again to trigger the self-delete.
void KpmProcess::detachProcess() {
  if (process == nullptr) {
    return;
  }
  QProcess *p = process;
  process = nullptr; // readStdout/readStderr stop seeing it from this instant
  p->disconnect(this);
  p->setParent(nullptr);
  if (p->state() == QProcess::NotRunning) {
    p->deleteLater();
    return;
  }
  QObject::connect(p, QOverload<int, QProcess::ExitStatus>::of(&QProcess::finished), p, &QObject::deleteLater);
  QObject::connect(p, static_cast<void (QProcess::*)(QProcess::ProcessError)>(&QProcess::error), p,
                   &QObject::deleteLater);
  p->kill();
}

// reportLater defers a run's terminal report (dialog + failure + finish) to the
// next event-loop turn. Nickel's showErrorDialog may spin a NESTED event loop, and
// these callers reach it from inside a live QProcess emission with the channel
// notifiers still active: QProcess's own death handling then runs inside that
// nested loop and destroys a notifier whose activation is still on the stack — a
// use-after-free inside Qt 5.2.1 that takes Nickel down rather than merely
// freezing it. Deferring puts the dialog on a clean stack with no QProcess frame
// under it.
//
// `this` is the context object and is guaranteed alive: finish() (the only thing
// that schedules our destruction) runs INSIDE the deferred call, and the Wi-Fi
// signals are already severed by the time a process exists.
//
// processFinished is deliberately NOT routed through here. There the child is
// already dead and fully cleaned up before finished() is emitted, so no notifier
// can be destroyed under an activation, and it is the most-travelled error path in
// the hook — shipped and device-validated. This is a targeted fix, not a sweep.
void KpmProcess::reportLater(const QString &dialogText, const QString &reason) {
  QTimer::singleShot(0, this, [this, dialogText, reason] {
    ConfirmationDialogFactory__showErrorDialog("kpm", dialogText);
    failure(reason);
    finish();
  });
}

void KpmProcess::watchdogExpired() {
  if (done) {
    return;
  }
  done = true;
  nh_log("KpmProcess::watchdogExpired()");
  detachProcess();
  reportLater("kpm took too long and was stopped.", "timeout");
}

// readStdout appends whatever kpm has written so far and terminates the run if
// the accumulated output blows kMaxStdoutBytes. Draining incrementally is what
// makes the cap real: reading only at finish would let the QProcess buffer grow
// unbounded inside Nickel first.
void KpmProcess::readStdout() {
  if (done || process == nullptr) {
    return;
  }
  stdoutBuf += process->readAllStandardOutput();
  if (stdoutBuf.size() <= kMaxStdoutBytes) {
    return;
  }
  done = true;
  if (watchdog != nullptr) {
    watchdog->stop();
  }
  // Detach rather than destroy: this runs INSIDE QProcess's
  // readyReadStandardOutput emission with the channel notifiers live, which is
  // precisely the stack a nested dialog loop must not sit on. reportStdoutOverflow
  // raises the dialog on the next turn (see detachProcess / reportLater).
  detachProcess();
  reportStdoutOverflow();
}

// readStderr drains kpm's stderr as it arrives. Reading is unconditional — a pipe
// left unread eventually blocks the child — but only the head up to kMaxStderrBytes
// is retained; the rest is discarded on the floor.
void KpmProcess::readStderr() {
  if (done || process == nullptr) {
    return;
  }
  appendStderr(process->readAllStandardError());
}

// utf8CutLen returns the largest length <= max at which `bytes` may be cut without
// splitting a multi-byte UTF-8 sequence. Cutting mid-sequence costs a replacement
// character at the tail of the retained text (cosmetic, but free to avoid): walk
// back off any continuation bytes (10xxxxxx) to the sequence boundary.
static int utf8CutLen(const QByteArray &bytes, int max) {
  if (max <= 0) {
    return 0;
  }
  if (max >= bytes.size()) {
    return bytes.size();
  }
  int n = max;
  while (n > 0 && (static_cast<unsigned char>(bytes.at(n)) & 0xC0) == 0x80) {
    n--;
  }
  return n;
}

// elide clips a QString to a character budget and appends the ellipsis, without
// cutting between the halves of a UTF-16 surrogate pair (one code point, two
// QChars — a split renders as a replacement character).
static QString elide(const QString &text, int maxChars) {
  QString out = text.left(maxChars);
  if (!out.isEmpty() && out.at(out.size() - 1).isHighSurrogate()) {
    out.chop(1);
  }
  return out.trimmed() + QStringLiteral("…");
}

// appendStderr keeps the head of a stderr chunk, up to kMaxStderrBytes total, and
// drops the remainder. The head is the useful part: kpm's lock message and its first
// failure lines come first, and nothing downstream reads more than a few hundred
// bytes.
void KpmProcess::appendStderr(const QByteArray &chunk) {
  int room = kMaxStderrBytes - stderrBuf.size();
  if (room > 0) {
    stderrBuf += chunk.left(utf8CutLen(chunk, room));
  }
}

// reportStdoutOverflow ends a run whose stdout blew the cap: the buffer is dropped
// unparsed and a hard error is surfaced. Shared by the streaming path above (which
// has detached the live process first) and the final drain in processFinished
// (where the process is already gone). The report itself is deferred either way —
// one idiom, and in the streaming case a mandatory one.
void KpmProcess::reportStdoutOverflow() {
  nh_log("KpmProcess: kpm wrote more than %d bytes to stdout", kMaxStdoutBytes);
  stdoutBuf.clear();
  stdoutBuf.squeeze(); // release the capped allocation now, not at deleteLater
  reportLater("kpm produced too much output and was stopped.", "overflow");
}

// processErrored handles exec-level failures (missing/non-executable binary,
// out of memory, crash). Crashed also reaches processFinished as a CrashExit,
// so the done latch keeps the two from double-reporting.
void KpmProcess::processErrored(QProcess::ProcessError err) {
  if (done) {
    return;
  }
  done = true;
  nh_log("KpmProcess::processErrored(%d)", static_cast<int>(err));
  if (watchdog != nullptr) {
    watchdog->stop();
  }
  QString message = err == QProcess::FailedToStart ? "Failed to run kpm — is it installed?" : "kpm stopped unexpectedly.";
  // FailedToStart leaves nothing running (detachProcess deletes it outright), but
  // ReadError/WriteError arrive from a LIVE process's channel emission, so this
  // path gets the same detach-and-defer treatment as readStdout rather than
  // dropping a dialog onto a stack that still holds QProcess state.
  detachProcess();
  reportLater(message, message);
}

// payloadError returns the human sentence a --json run put in its payload: the
// first failed[] entry's error, else the top-level `error` string (JSON-OUTPUT.md
// §2.3, INTEGRITY.md §6.4). Empty when the payload carries neither, which is the
// signal to fall back to stderr.
static QString payloadError(const QJsonObject &payload) {
  for (const QJsonValue &v : payload.value("failed").toArray()) {
    QString e = v.toObject().value("error").toString().trimmed();
    if (!e.isEmpty()) {
      return e;
    }
  }
  return payload.value("error").toString().trimmed();
}

// processFinished parses kpm's output exactly like cli.cc::processFinished:
// everything before BEGIN_JSON is human log text (→ nh_log), everything after
// is the JSON payload (JSON-OUTPUT.md §1).
void KpmProcess::processFinished(int exitCode, QProcess::ExitStatus status) {
  if (done) {
    return;
  }
  done = true;
  if (watchdog != nullptr) {
    watchdog->stop();
  }

  // Read from the emitting process (cli.cc's sender() pattern) — with the
  // re-entry guard there is only ever one, but never trust the member blindly.
  QProcess *proc = qobject_cast<QProcess *>(sender());
  if (proc == nullptr) {
    proc = process;
  }

  // A crashed kpm has no meaningful exit code or payload — report, don't parse.
  if (status != QProcess::NormalExit) {
    nh_log("KpmProcess: kpm crashed");
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm stopped unexpectedly.");
    failure("crash");
    finish();
    return;
  }

  // Drain the tail readStdout has not seen (a short-lived process can deliver
  // finished() before its last readyReadStandardOutput), then re-check the cap:
  // that final chunk is the one path that can cross it without readStdout ever
  // running. The process is already gone here, so there is nothing to kill.
  stdoutBuf += proc->readAllStandardOutput();
  if (stdoutBuf.size() > kMaxStdoutBytes) {
    reportStdoutOverflow();
    return;
  }

  const QByteArray &out = stdoutBuf;
  int index = out.indexOf("BEGIN_JSON");

  QByteArray logBytes = index >= 0 ? out.left(index) : out;
  for (const QByteArray &line : logBytes.split('\n')) {
    if (!line.isEmpty()) {
      nh_log("%s", qPrintable(QString(line)));
    }
  }

  bool haveMarker = index >= 0;
  QJsonObject payload;
  if (haveMarker) {
    QByteArray json = out.mid(index + static_cast<int>(sizeof("BEGIN_JSON") - 1));
    payload = QJsonDocument::fromJson(json).object();
  }

  // exit 0 (ok) and exit 2 (partial) both carry a payload the caller renders;
  // the caller summarizes a partial run (§4). But ONLY when the BEGIN_JSON
  // marker is actually present: a Go panic exits 2 with no marker and no
  // payload, which must surface as an error rather than render an empty result
  // as success (JSON-OUTPUT §1 — every --json path ends on the marker).
  //
  // The one exception is a command that HAS no --json mode (expectsPayload=false,
  // currently only `kpm remove`): for those the marker never exists and exit 0 is
  // the whole result. Exit 2 is still an error there — that is the panic code.
  bool ok = haveMarker ? (exitCode == 0 || exitCode == 2) : (!expectsPayload && exitCode == 0);
  if (ok) {
    response(exitCode, payload);
    finish();
    return;
  }

  // Hard error: a non-zero exit, or a missing payload marker. Drain the tail
  // readStderr has not seen; appendStderr keeps only the head, so every string
  // built from here — the QString, the qPrintable temporary below, and the dialog
  // text — is bounded by kMaxStderrBytes rather than by how much a runaway kpm
  // decided to write.
  appendStderr(proc->readAllStandardError());
  QString stderrText = QString::fromUtf8(stderrBuf).trimmed();
  QString message = stderrText;
  // Prefer the PAYLOAD's own sentence when kpm emitted one. A failing --json run
  // still ends on BEGIN_JSON (JSON-OUTPUT.md §1), and its failed[0].error is the
  // authoritative, single-sentence reason — INTEGRITY.md §6.4, and the only place
  // §4.6's designated "conflicts with installed package" substring is contractually
  // guaranteed to appear. Reading stderr instead worked only because kpm happens to
  // print the same sentence there too; that is a coincidence, not a contract, and
  // stderr additionally carries the `kpm <cmd>: ` prefix and any WARN lines that
  // preceded the failure.
  if (haveMarker) {
    QString fromPayload = payloadError(payload);
    if (!fromPayload.isEmpty()) {
      message = fromPayload;
    }
  }
  // The lock message is emitted on stderr before any payload exists, so it is
  // matched against stderr specifically (§4).
  if (stderrText.contains("another kpm instance", Qt::CaseInsensitive)) {
    message = "kpm is busy — try again in a moment.";
  } else if (expectsPayload && !haveMarker) {
    // A --json command that ended without the marker violated the protocol (a Go
    // panic exits 2 with a stack trace on stderr): say so, rather than paging a
    // stack trace into a Nickel dialog.
    message = "kpm produced no result (exit code " + QString::number(exitCode) + ").";
  } else if (message.isEmpty()) {
    message = "kpm failed (exit code " + QString::number(exitCode) + ").";
  } else if (message.size() > kMaxDialogChars) {
    // Passthrough text (payload sentence or raw stderr) is the only branch that can
    // be long — the synthesized messages above are all a single line.
    message = elide(message, kMaxDialogChars);
  }
  nh_log("KpmProcess: error: %s", qPrintable(message));
  ConfirmationDialogFactory__showErrorDialog("kpm", message);
  failure(message);
  finish();
}
