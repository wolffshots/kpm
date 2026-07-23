// Adapted from NickelHardcover hook/src/widgets/dialog.cc (MIT,
// https://codeberg.org/StrayRose/NickelHardcover) per NICKEL-UI.md §0/§5.
// The ReadingView-only auto-close (currentViewChanged / SyncController) is
// dropped; the dialog is dismissed only via its close button (closeTapped).

#include <QApplication>
#include <QScreen>
#include <QTimer>

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

  // Win 1 (kpm-flash-reduction-plan): hide the status/nav bars BETWEEN pushView
  // and show(), not after, so the dialog's first paint already has the chrome
  // gone — one flash on open instead of two (paint-with-bars, then paint-without).
  // hideChrome finds its targets independently of this dialog being shown:
  // MainNavView via findWidgetByClassName over all top-level widgets, and the
  // status bar via MainWindowController__statusBarController(mwc) — neither needs
  // our view visible — so calling it pre-show() is sound. The nh_log line inside
  // confirms on-device that both bars are still found when hidden this early.
  if (canHideChrome) {
    hideChrome(mwc);
  }

  dialog->show();
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

  // Sleep/wake tears down the power view and re-asserts the home chrome
  // (showStatusBar + re-show MainNavView), clobbering the hide above. Install an
  // app-wide filter that reacts to that symptom and re-hides. Only the recording
  // dialog (which actually hid something) installs one; stacked dialogs that
  // recorded nothing skip it and their handler never runs.
  if (hidStatusBar || hiddenNavView) {
    chromeGuard = new ChromeGuard(this);
    QApplication::instance()->installEventFilter(chromeGuard);
    nh_log("NickelKPM: chrome guard installed (app event filter)");
  }
}

// reassertChrome re-hides the home-screen chrome Nickel re-showed behind our
// back. `shownNav` is the MainNavView whose Show event just fired. It never
// calls show() — only re-hides (idempotent) — and refreshes hiddenNavView so
// ~Dialog restores the live widget even if Nickel recreated it. hidStatusBar
// semantics are unchanged: the handler re-hides the bar but does not touch the
// recorded flag.
void Dialog::reassertChrome(QWidget *shownNav) {
  bool navRefound = (hiddenNavView != shownNav);
  hiddenNavView = shownNav; // Nickel may have recreated it; aim ~Dialog's restore here

  bool navReHidden = false;
  if (hiddenNavView->isVisible()) {
    hiddenNavView->hide();
    navReHidden = true;
  }
  bool statusReHidden = reassertStatusBar();

  // Two fragilities the immediate re-hide above does not fully cover: re-hiding
  // inside the Show delivery can be clobbered by the completing show(), and wake
  // re-shows the bar and the nav in either order (a status bar re-shown just AFTER
  // this nav Show is missed). Queue a re-assert for the end of the event-loop turn
  // as a safety net; `this` cancels it if the dialog closes first.
  QTimer::singleShot(0, this, [this] {
    if (hiddenNavView && hiddenNavView->isVisible()) {
      hiddenNavView->hide();
    }
    reassertStatusBar();
  });

  nh_log("NickelKPM: chrome guard fired — nav %s (%s), status bar %s",
         navRefound ? "re-found (recreated)" : "same widget", navReHidden ? "re-hidden" : "already hidden",
         statusReHidden ? "re-hidden" : "already hidden");
}

// reassertStatusBar re-hides the status bar iff we recorded hiding it. Symbol-
// guarded and idempotent (isVisible short-circuits). Returns whether it hid.
bool Dialog::reassertStatusBar() {
  if (!hidStatusBar || !MainWindowController__statusBarController || !StatusBarController__hideStatusBar ||
      !StatusBarController__isVisible) {
    return false;
  }
  MainWindowController *mwc = MainWindowController__sharedInstance();
  StatusBarController *sbc = MainWindowController__statusBarController(mwc);
  if (sbc && StatusBarController__isVisible(sbc)) {
    StatusBarController__hideStatusBar(sbc);
    return true;
  }
  return false;
}

Dialog::~Dialog() {
  // Remove the guard FIRST, before restoring — otherwise the nav->show() /
  // showStatusBar() below would re-trigger the filter and re-hide what we are
  // restoring. Dropping ownership here makes the restore fire exactly once.
  if (chromeGuard) {
    QApplication::instance()->removeEventFilter(chromeGuard);
    delete chromeGuard;
    chromeGuard = nullptr;
  }
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

ChromeGuard::ChromeGuard(Dialog *owner) : QObject(owner), owner(owner) {}

bool ChromeGuard::eventFilter(QObject *obj, QEvent *event) {
  if (event->type() == QEvent::Show || event->type() == QEvent::ShowToParent) {
    if (QWidget *w = qobject_cast<QWidget *>(obj)) {
      if (!qstrcmp(w->metaObject()->className(), "MainNavView")) {
        owner->reassertChrome(w);
      }
    }
  }
  return QObject::eventFilter(obj, event);
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
