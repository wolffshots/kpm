// NICKEL-UI.md §4 — KpmProcess implementation, adapted from NickelHardcover's
// cli.cc (MIT). The Wi-Fi bring-up (constructor + connectingFailed + the
// singleShot-yield) is copied near-verbatim; the output protocol matches
// kpm 0.6.0's --json mode (JSON-OUTPUT.md §1: human log lines, then a final
// BEGIN_JSON{...} line).

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

bool KpmProcess::busy() { return s_mutatingInFlight; }

KpmProcess *KpmProcess::start(QStringList arguments, bool needsNetwork, bool mutating) {
  if (mutating && s_mutatingInFlight) {
    nh_log("KpmProcess: refusing to start a second mutating command");
    return nullptr;
  }
  return new KpmProcess(arguments, needsNetwork, mutating);
}

// ---- lifecycle + Wi-Fi gate (copied from cli.cc) ------------------------

KpmProcess::KpmProcess(QStringList arguments, bool needsNetwork, bool mutating, QObject *parent)
    : QObject(parent), arguments(arguments), needsNetwork(needsNetwork), mutating(mutating) {
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

void KpmProcess::watchdogExpired() {
  if (done) {
    return;
  }
  done = true;
  nh_log("KpmProcess::watchdogExpired()");
  if (process != nullptr) {
    // Sever the process's signals first so the kill's queued finished()/error()
    // can't re-enter and report the timed-out run as a success.
    process->disconnect(this);
    process->kill();
  }
  ConfirmationDialogFactory__showErrorDialog("kpm", "kpm took too long and was stopped.");
  failure("timeout");
  finish();
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
  ConfirmationDialogFactory__showErrorDialog("kpm", message);
  failure(message);
  finish();
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

  QByteArray out = proc->readAllStandardOutput();
  int index = out.indexOf("BEGIN_JSON");

  QByteArray logBytes = index >= 0 ? out.left(index) : out;
  for (const QByteArray &line : logBytes.split('\n')) {
    if (!line.isEmpty()) {
      nh_log("%s", qPrintable(QString(line)));
    }
  }

  QJsonObject payload;
  if (index >= 0) {
    QByteArray json = out.mid(index + static_cast<int>(sizeof("BEGIN_JSON") - 1));
    payload = QJsonDocument::fromJson(json).object();
  }

  // exit 0 (ok) and exit 2 (partial) both carry a payload the caller renders;
  // the caller summarizes a partial run (§4).
  if (exitCode == 0 || exitCode == 2) {
    response(exitCode, payload);
    finish();
    return;
  }

  // Any other non-zero exit is a hard error: surface stderr, mapping kpm's
  // single-instance-lock message to a friendly "busy" dialog (§4).
  QByteArray err = proc->readAllStandardError();
  QString message = QString::fromUtf8(err).trimmed();
  if (message.contains("another kpm instance", Qt::CaseInsensitive)) {
    message = "kpm is busy — try again in a moment.";
  } else if (message.isEmpty()) {
    message = "kpm failed (exit code " + QString::number(exitCode) + ").";
  }
  nh_log("KpmProcess: error: %s", qPrintable(message));
  ConfirmationDialogFactory__showErrorDialog("kpm", message);
  failure(message);
  finish();
}
