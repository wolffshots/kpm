#pragma once

// NICKEL-UI.md §4 — KpmProcess: the hook↔kpm contract.
//
// Modeled on NickelHardcover's cli.cc (MIT): runs the installed kpm binary via
// QProcess, streams its human log lines to nh_log, and parses the final
// BEGIN_JSON payload. Adds a Wi-Fi bring-up gate for network commands and a
// static in-flight guard so only one mutating kpm runs at a time.

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

private:
  KpmProcess(QStringList arguments, bool needsNetwork, bool mutating, QObject *parent = nullptr);
  ~KpmProcess();

  static KpmProcess *start(QStringList arguments, bool needsNetwork, bool mutating);

  void showIcon(const char *path);
  void finish();
  void disconnectWifiSignals();

  QStringList arguments;
  bool needsNetwork;
  bool mutating;

  QLabel *icon = nullptr;
  QTimer *timer = nullptr;    // Wi-Fi connect timeout
  QTimer *watchdog = nullptr; // process watchdog
  QProcess *process = nullptr;

  // done latches on the FIRST terminal outcome (finished, exec error, timeout,
  // Wi-Fi failure); every later signal delivery is ignored, so a killed
  // process's queued finished() can never be reported as success and a Wi-Fi
  // flap can never tear down a run that already reached a terminal state.
  bool done = false;

  static bool s_mutatingInFlight;
};
