// Adapted from NickelHardcover hook/src/widgets/dialog.cc (MIT,
// https://codeberg.org/StrayRose/NickelHardcover) per NICKEL-UI.md §0/§5.
// The ReadingView-only auto-close (currentViewChanged / SyncController) is
// dropped; the dialog is dismissed only via its close button (closeTapped).

#include <QApplication>
#include <QScreen>

#include <NickelHook.h>

#include "dialog.h"

Dialog::Dialog(QString title) : QFrame() {
  dialog = N3DialogFactory__getDialog(this, true);
  N3Dialog__setTitle(dialog, title);

  // Ask Nickel to treat the dialog as a full view (NickelHardcover's
  // JournalDialog does the same). On 4.45 this improves presentation but does
  // NOT stop the home screen's status/nav bars drawing over the dialog — that
  // is handled by hideChrome() below. Optional symbol.
  if (N3Dialog__enableFullViewMode) {
    N3Dialog__enableFullViewMode(dialog);
    nh_log("NickelKPM: full view mode enabled");
  } else {
    nh_log("NickelKPM: N3Dialog::enableFullViewMode unavailable on this firmware");
  }

  // The dialog is full-screen (NickelHardcover pattern), but on the home screen
  // a persistent status bar (top) and nav bar (bottom) draw over it, hiding our
  // title bar and the keyboard's bottom row (NH only ever opens from the
  // reader, which has neither). Preferred fix: hide both bars while the dialog
  // is open (hideChrome below, restored in ~Dialog). Fallback when the firmware
  // lacks the status-bar symbols: capture the current content view's on-screen
  // rect BEFORE pushing our view and expose the difference as `chrome` insets
  // for the subclass layouts.
  MainWindowController *mwc = MainWindowController__sharedInstance();
  QRect screenGeometry = QApplication::primaryScreen()->geometry();
  bool canHideChrome = MainWindowController__statusBarController && StatusBarController__hideStatusBar &&
                       StatusBarController__showStatusBar && StatusBarController__isVisible;
  if (!canHideChrome) {
    if (QWidget *cv = MainWindowController__currentView(mwc)) {
      QPoint tl = cv->mapToGlobal(QPoint(0, 0));
      int top = tl.y() - screenGeometry.top();
      int bottom = screenGeometry.bottom() - (tl.y() + cv->height() - 1);
      if (top > 0 && top < screenGeometry.height()) {
        chrome.setTop(top);
      }
      if (bottom > 0 && bottom < screenGeometry.height()) {
        chrome.setBottom(bottom);
      }
    }
    nh_log("NickelKPM: chrome insets top=%d bottom=%d (screen %dx%d)", chrome.top(), chrome.bottom(),
           screenGeometry.width(), screenGeometry.height());
  }
  dialog->setFixedSize(screenGeometry.width(), screenGeometry.height());

  MainWindowController__pushView(mwc, dialog);

  QObject::connect(dialog, SIGNAL(closeTapped()), dialog, SLOT(deleteLater()));

  dialog->show();

  if (canHideChrome) {
    hideChrome(mwc);
  }
}

// findWidgetByClassName walks every widget in the process looking for Nickel's
// `name` (Qt meta-object class name). Used for the nav bar: unlike the status
// bar there is no exported controller method to hide it, but MainNavView is an
// ordinary QWidget in the main window's tree.
static QWidget *findWidgetByClassName(char const *name) {
  for (QWidget *top : QApplication::topLevelWidgets()) {
    if (!qstrcmp(top->metaObject()->className(), name)) {
      return top;
    }
    for (QWidget *w : top->findChildren<QWidget *>()) {
      if (!qstrcmp(w->metaObject()->className(), name)) {
        return w;
      }
    }
  }
  return nullptr;
}

// hideChrome hides the status bar (via its controller) and the bottom nav bar
// (by widget) while the dialog is open, recording exactly what was hidden so
// ~Dialog restores only that. A DetailDialog opened over the BrowseDialog finds
// both already hidden and records nothing, so the browse dialog's destructor is
// the one that restores. Called only when the optional symbols resolved.
void Dialog::hideChrome(MainWindowController *mwc) {
  StatusBarController *sbc = MainWindowController__statusBarController(mwc);
  if (sbc && StatusBarController__isVisible(sbc)) {
    StatusBarController__hideStatusBar(sbc);
    hidStatusBar = true;
  }

  QWidget *nav = findWidgetByClassName("MainNavView");
  if (nav && nav->isVisible()) {
    nav->hide();
    hiddenNavView = nav;
  } else if (!nav) {
    // Log the tree roots so a renamed nav class on future firmware is
    // diagnosable from logread.
    for (QWidget *top : QApplication::topLevelWidgets()) {
      nh_log("NickelKPM: top-level widget: %s", top->metaObject()->className());
    }
  }

  nh_log("NickelKPM: chrome hidden (status bar: %s, nav view: %s)", hidStatusBar ? "yes" : "no",
         hiddenNavView ? "yes" : (nav ? "already hidden" : "not found"));
}

Dialog::~Dialog() {
  if (hidStatusBar) {
    MainWindowController *mwc = MainWindowController__sharedInstance();
    if (StatusBarController *sbc = MainWindowController__statusBarController(mwc)) {
      StatusBarController__showStatusBar(sbc);
    }
  }
  if (hiddenNavView) {
    hiddenNavView->show();
  }
}

void Dialog::showKeyboard() { N3Dialog__showKeyboard(dialog); }

void Dialog::hideKeyboard() { N3Dialog__hideKeyboard(dialog); }

KeyboardFrame *Dialog::buildKeyboardFrame(TouchLineEdit *lineEdit, QString goText) {
  KeyboardReceiver *receiver = construct_KeyboardReceiver(lineEdit);
  KeyboardFrame *keyboard = buildKeyboardFrame(receiver, goText);
  QObject::connect(lineEdit, SIGNAL(tapped()), this, SLOT(showKeyboard()));

  return keyboard;
}

KeyboardFrame *Dialog::buildKeyboardFrame(KeyboardReceiver *receiver, QString goText) {
  KeyboardFrame *keyboard = N3Dialog__keyboardFrame(dialog);

  SearchKeyboardController *ctl = KeyboardFrame__createKeyboard(keyboard, 0, locale());
  SearchKeyboardController__setReceiver(ctl, receiver, false);
  SearchKeyboardController__setGoText(ctl, goText);

  QObject::connect(ctl, SIGNAL(commitRequested()), this, SLOT(hideKeyboard()));
  QObject::connect(ctl, SIGNAL(commitRequested()), this, SLOT(commit()));

  return keyboard;
}
