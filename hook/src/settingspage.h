#pragma once

// The Settings tab of the package manager: where kpm opens from (the row on the
// More page or NickelMenu), and whether the NickelMenu button hides. The hook
// owns these settings (launch.h). The "Hide in library" row shows and sets
// ExcludeSyncFolders through `kpm exclude-sync` (EXCLUDE-SYNC.md section 5).

#include <QJsonObject>
#include <QWidget>

#include "launch.h"
#include "nkpm.h"

class Label;

class SettingsPage : public QWidget {
  Q_OBJECT

public:
  explicit SettingsPage(QWidget *parent = nullptr);

  // load reads the settings and fills the page in place.
  void load();

public Q_SLOTS:
  void chooseMoreRow();
  void chooseNickelMenu();
  void toggleHide();
  void chooseDotFolders();
  void chooseSyncOff();

private:
  // loadSync runs `kpm exclude-sync --json` and fills the row from its payload.
  void loadSync();
  void applySync(const QJsonObject &payload);
  void setSyncBusy(bool busy);
  // runSync runs a set or off and reads the row again after it.
  void runSync(class KpmProcess *p);

  // save writes the settings and makes the NickelMenu files match them.
  void save(struct nkpm_launch const &next);

  struct nkpm_launch settings = {1, 0};
  bool nmInstalled = false;
  bool rowAvailable = false;

  N3ButtonLabel *moreButton = nullptr;
  N3ButtonLabel *nmButton = nullptr;
  Label *openNote = nullptr;
  Label *hideName = nullptr;
  Label *hideValue = nullptr;
  N3ButtonLabel *hideButton = nullptr;
  Label *hideNote = nullptr;

  // The "Hide in library" row. syncSource is the "source" of the last payload.
  QString syncSource;
  bool syncBusy = false;
  bool syncChanged = false; // a set or off ran since the page was made
  Label *syncName = nullptr;
  N3ButtonLabel *dotButton = nullptr;
  N3ButtonLabel *syncOffButton = nullptr;
  Label *syncNote = nullptr;
};
