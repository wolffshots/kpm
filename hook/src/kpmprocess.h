#pragma once

// NICKEL-UI.md §4 — KpmProcess: the hook↔kpm contract.
//
// Modeled on NickelHardcover's cli.cc (MIT): runs the installed kpm binary via
// QProcess, streams its human log lines to nh_log, and parses the final
// BEGIN_JSON payload. Adds a Wi-Fi bring-up gate for network commands and a
// static in-flight guard so only one mutating kpm runs at a time.

#include <QByteArray>
#include <QJsonObject>
#include <QLabel>
#include <QObject>
#include <QProcess>
#include <QStringList>

class QTimer;

class KpmProcess : public QObject {
  Q_OBJECT

public:
  // Factories mapping to the kpm commands the UI drives (NICKEL-UI.md §4/§6).
  // Browse reads (search) need neither network nor the mutating guard. Mutating
  // factories return nullptr when another mutating kpm is already in flight —
  // the caller shows a "busy" state and does not launch.
  static KpmProcess *search();                    // kpm search --json (no network)
  static KpmProcess *check();                      // kpm check --json (network; takes kpm's lock → serialized)
  static KpmProcess *registryRefresh();            // kpm registry refresh --json (network, mutating)
  static KpmProcess *install(const QString &id);   // kpm install <id> --yes --json (network, mutating)
  static KpmProcess *update(const QString &id);    // kpm update <id> --json (network, mutating; update has no --yes)
  static KpmProcess *updateAll();                  // kpm update --all --json (network, mutating)
  static KpmProcess *uninstall(const QString &id); // kpm uninstall <id> --yes --json (mutating)
  static KpmProcess *list();                       // kpm list --json (no network) — which ids are REGISTERED
  // remove unregisters a def without deleting files (INTEGRITY.md §4.3's orphaned
  // def). It is the only command here with no --json mode, so it is started with
  // expectsPayload=false and its response() carries an empty payload.
  static KpmProcess *remove(const QString &id);    // kpm remove <id> (mutating, no network)
  static KpmProcess *sync();                        // kpm sync --json (mutating; no network — reads the registry cache)
  static KpmProcess *adoptSelf();                   // kpm adopt-self --json (network, mutating; enrols kpm's self-update)

  // Config editing (CONFIG.md §3.2, contract uicontract blocks 8-10). list/show
  // are read-only offline reads (no lock, no network); set is mutating (rides the
  // single-instance lock) but never touches the network — configs are local files.
  static KpmProcess *configList(const QString &id);                  // kpm config list <id> --json
  static KpmProcess *configShow(const QString &id, const QString &selector); // kpm config show <id> <sel> --json
  // configInit seeds a missing file from its declared template (CONFIG.md §3.x).
  // Mutating (rides the single-instance lock), no network — a local file write.
  static KpmProcess *configInit(const QString &id, const QString &selector); // kpm config init <id> <sel> --json
  // set variants map 1:1 to the CLI's ini (--key/--section) and text
  // (--line/--append/--delete) edit forms (cmd_config.go applyEdit).
  static KpmProcess *configSetKey(const QString &id, const QString &selector, const QString &section,
                                  const QString &key, const QString &value);
  static KpmProcess *configSetLine(const QString &id, const QString &selector, int line, const QString &value);
  static KpmProcess *configAppendLine(const QString &id, const QString &selector, const QString &value);
  static KpmProcess *configDeleteLine(const QString &id, const QString &selector, int line);

  // busy reports whether a mutating kpm is currently running (§4).
  static bool busy();

Q_SIGNALS:
  // response carries the parsed BEGIN_JSON payload with kpm's exit code
  // (0 ok, 2 partial). The caller treats the exit code as authoritative and the
  // payload as best-effort detail (JSON-OUTPUT.md §1).
  void response(int exitCode, QJsonObject payload);
  // failure is emitted for a hard error (network bring-up failed, non-zero exit
  // with no payload, process error, or timeout). KpmProcess shows the error
  // dialog itself before emitting, so the caller only needs to re-enable UI.
  void failure(QString reason);

public Q_SLOTS:
  void networkConnected();
  void connectingFailed();
  void processFinished(int exitCode, QProcess::ExitStatus status);
  void processErrored(QProcess::ProcessError err);
  void watchdogExpired();
  // readStdout drains kpm's stdout as it arrives and enforces the size cap.
  // QProcess buffers a child's output until it is read, so reading only in
  // processFinished would let a runaway kpm grow Nickel's heap without bound
  // before the hook ever saw a byte. See kpmprocess.cc.
  void readStdout();
  // readStderr drains kpm's stderr as it arrives, retaining only a bounded head.
  // Same accumulation hazard as stdout, one channel over; see kpmprocess.cc.
  void readStderr();

private:
  KpmProcess(QStringList arguments, bool needsNetwork, bool mutating, bool expectsPayload, QObject *parent = nullptr);
  ~KpmProcess();

  static KpmProcess *start(QStringList arguments, bool needsNetwork, bool mutating, bool expectsPayload = true);

  void showIcon(const char *path);
  void finish();
  void disconnectWifiSignals();
  void reportStdoutOverflow();
  void appendStderr(const QByteArray &chunk);
  // detachProcess kills the child after severing it from this object AND from
  // Nickel's UI lifetime, so ~QProcess's 30s waitForFinished can never run on the
  // UI thread. See kpmprocess.cc.
  void detachProcess();
  // reportLater defers a terminal report (dialog + failure + finish) one event-loop
  // turn, so a nested dialog loop is never entered from inside a live QProcess
  // emission. See kpmprocess.cc.
  void reportLater(const QString &dialogText, const QString &reason);

  QStringList arguments;
  bool needsNetwork;
  bool mutating;
  // expectsPayload is false only for a command with no --json mode (kpm remove):
  // there, exit 0 with no BEGIN_JSON marker is success rather than a protocol
  // violation. True for all fourteen --json commands, which keep the strict rule.
  bool expectsPayload;

  QLabel *icon = nullptr;
  QTimer *timer = nullptr;    // Wi-Fi connect timeout
  QTimer *watchdog = nullptr; // process watchdog
  QProcess *process = nullptr;

  // Accumulated stdout, drained incrementally by readStdout() so the size cap is
  // enforced while kpm runs rather than after it has already ballooned.
  QByteArray stdoutBuf;
  // Accumulated stderr, capped to its head by appendStderr(). Bounds not just the
  // buffer but everything derived from it — including the string handed to a Nickel
  // error dialog.
  QByteArray stderrBuf;

  // done latches on the FIRST terminal outcome (finished, exec error, timeout,
  // Wi-Fi failure); every later signal delivery is ignored, so a killed
  // process's queued finished() can never be reported as success and a Wi-Fi
  // flap can never tear down a run that already reached a terminal state.
  bool done = false;

  static bool s_mutatingInFlight;
};
