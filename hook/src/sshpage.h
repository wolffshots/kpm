#pragma once

// kpm-archive/docs/SSH.md section 7: the SSH tab of the package manager. It is
// a page of its own in BrowseDialog, not a filter of the package list. It reads
// `kpm ssh status --json` (JSON-OUTPUT.md section 2.7) and drives
// `kpm ssh enable|disable|restart`.

#include <QJsonObject>
#include <QWidget>

#include "nkpm.h"

class Label;
class QGridLayout;

class SshPage : public QWidget {
  Q_OBJECT

public:
  explicit SshPage(QWidget *parent = nullptr);

  // load runs `kpm ssh status --json`. The page fills its rows in place when
  // the status arrives, then emits loaded(). BrowseDialog shows the page only
  // then, so the first show is the full page (no "Loading" frame).
  void load();

Q_SIGNALS:
  void loaded();

public Q_SLOTS:
  void toggle();
  void restart();
  void onStatus(int exitCode, QJsonObject payload);
  void onActionDone(int exitCode, QJsonObject payload);
  void onFailure(QString reason);

private:
  Label *addRow(QGridLayout *grid, int row, const QString &name, Label **nameOut = nullptr);
  void apply(const QJsonObject &status);
  void setBusy(bool busy);

  Label *enabledValue = nullptr;
  Label *statusValue = nullptr;
  Label *addressValue = nullptr;
  Label *hostKeyValue = nullptr;
  Label *keysValue = nullptr;
  Label *errorName = nullptr;
  Label *errorValue = nullptr;
  Label *noKeyNote = nullptr;
  N3ButtonLabel *toggleButton = nullptr;
  N3ButtonLabel *restartButton = nullptr;

  bool enabled = false;
  bool busy = false;
};
