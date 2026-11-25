#pragma once

// The Settings tab of the package manager: where kpm opens from (the row on the
// More page or NickelMenu), and whether the NickelMenu button hides. The hook
// owns these settings (launch.h), so the page runs no kpm command.

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

private:
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
};
