#pragma once

// Adapted from NickelHardcover hook/src/widgets/dialog.h (MIT,
// https://codeberg.org/StrayRose/NickelHardcover) per NICKEL-UI.md §0/§5.
// Changes: NickelKPM is launched from the home menu, not a book, so NH's
// ReadingView-only auto-close (currentViewChanged / SyncController) is dropped;
// the dialog closes only when the user taps its close button (closeTapped).

#include <QFrame>
#include <QTextEdit>

#include "../nkpm.h"

class Dialog : public QFrame {
  Q_OBJECT

public Q_SLOTS:
  void showKeyboard();
  void hideKeyboard();

  virtual void commit() {}

protected:
  Dialog(QString title);

  N3Dialog *dialog = nullptr;

  KeyboardFrame *buildKeyboardFrame(TouchLineEdit *lineEdit, QString goText);

private:
  KeyboardFrame *buildKeyboardFrame(KeyboardReceiver *receiver, QString goText);
};
