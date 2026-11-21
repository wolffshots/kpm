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

  // hideAddress hides the IP address of the Address row again (SSH.md
  // section 7). BrowseDialog calls it each time the tab opens.
  void hideAddress();

Q_SIGNALS:
  void loaded();

public Q_SLOTS:
  void toggle();
  void restart();
  void onStatus(int exitCode, QJsonObject payload);
  void onActionDone(int exitCode, QJsonObject payload);
  void onFailure(QString reason);
  void toggleAddress();
  void toggleForceWifi();

protected:
  void showEvent(QShowEvent *event) override;

private:
  Label *addRow(QGridLayout *grid, int row, const QString &name, Label **nameOut = nullptr,
                QWidget *extra = nullptr);
  void apply(const QJsonObject &status);
  void updateAddress();
  void updateForceWifi();
  bool darkPage() const;
  void setBusy(bool busy);

  Label *enabledValue = nullptr;
  Label *statusValue = nullptr;
  Label *addressValue = nullptr;
  Label *hostKeyValue = nullptr;
  Label *keysValue = nullptr;
  Label *errorName = nullptr;
  Label *errorValue = nullptr;
  Label *noKeyNote = nullptr;
  TouchLabel *eyeButton = nullptr;
  Label *wifiName = nullptr;
  Label *wifiValue = nullptr;
  Label *wifiNote = nullptr;
  N3ButtonLabel *wifiButton = nullptr;
  N3ButtonLabel *toggleButton = nullptr;
  N3ButtonLabel *restartButton = nullptr;

  QString address; // root@<ip>:<port>, or empty when the device has no address
  bool addressShown = false;
  int eyeState = -1; // the size and variant of the eye pixmap on screen
  bool enabled = false;
  bool busy = false;
};
