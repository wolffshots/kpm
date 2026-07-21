// NICKEL-UI.md §1/§3 — NickelKPM hook entry point and Nickel symbol tables.
//
// Structure and the Nickel symbol set are adapted from NickelHardcover's
// nickelhardcover.cc (MIT, https://codeberg.org/StrayRose/NickelHardcover).
// The More-menu ("main" menu) injection technique is replicated minimally from
// pgaskin/NickelMenu's src/nickelmenu.cc (see §3): NickelKPM appends exactly one
// static "Package manager" row and marks every entry-point hook optional so a
// firmware symbol miss degrades to "no entry point" rather than a failed load.

#include <QAction>
#include <QCoreApplication>
#include <QMenu>
#include <QProcess>
#include <QTimer>
#include <QWidget>

#include <unistd.h>

#include <NickelHook.h>

#include "browsedialog.h"
#include "files.h"
#include "nkpm.h"

// ---- shared Nickel symbols (definitions for nkpm.h) ---------------------

MainWindowController *(*MainWindowController__sharedInstance)();
QWidget *(*MainWindowController__currentView)(MainWindowController *mwc);
QWidget *(*MainWindowController__pushView)(MainWindowController *mwc, QWidget *view);

void (*ConfirmationDialogFactory__showErrorDialog)(QString const &title, QString const &body);
ConfirmationDialog *(*ConfirmationDialogFactory__getConfirmationDialog)(QWidget *parent);
void (*ConfirmationDialog__setTitle)(ConfirmationDialog *_this, QString const &);
void (*ConfirmationDialog__setText)(ConfirmationDialog *_this, QString const &);
void (*ConfirmationDialog__setAcceptButtonText)(ConfirmationDialog *_this, QString const &);
void (*ConfirmationDialog__setRejectButtonText)(ConfirmationDialog *_this, QString const &);

WirelessWorkflowManager *(*WirelessWorkflowManager__sharedInstance)();
bool (*WirelessWorkflowManager__isInternetAccessible)(WirelessWorkflowManager *);
void (*WirelessWorkflowManager__connectWirelessSilently)(WirelessWorkflowManager *);
void (*WirelessWorkflowManager__connectWireless)(WirelessWorkflowManager *_this, bool, bool);
WirelessManager *(*WirelessManager__sharedInstance)();

SearchKeyboardController *(*KeyboardFrame__createKeyboard)(KeyboardFrame *__this, int keyboardScript, QLocale locale);
void (*SearchKeyboardController__setReceiver)(SearchKeyboardController *__this, KeyboardReceiver *receiver, bool idk);
void (*SearchKeyboardController__setGoText)(SearchKeyboardController *__this, QString const &text);

N3Dialog *(*N3DialogFactory__getDialog)(QWidget *content, bool idk);
void (*N3Dialog__setTitle)(N3Dialog *__this, QString const &);
KeyboardFrame *(*N3Dialog__keyboardFrame)(N3Dialog *__this);
void (*N3Dialog__showKeyboard)(N3Dialog *__this);
void (*N3Dialog__hideKeyboard)(N3Dialog *__this);

void (*TouchLabel__setHitStateEnabled)(TouchLabel *_this, bool enabled);

// ---- constructors (file-local; used by the construct_* helpers) ---------
//
// Each calloc must be at least the real (unknown, firmware-dependent) Nickel
// class size or the constructor corrupts the heap. 1024 is deliberate headroom
// against object growth in future firmware; the cost is only slack bytes.

static void (*TouchLineEdit__constructor)(TouchLineEdit *__this, QWidget *parent);
static void (*TouchLabel__constructor)(TouchLabel *_this, QWidget *parent, QFlags<Qt::WindowType>);
static void (*N3ButtonLabel__constructor)(N3ButtonLabel *_this, QWidget *parent);
static void (*KeyboardReceiver__constructor)(KeyboardReceiver *__this, QLineEdit *lineEdit, bool unknown);

TouchLineEdit *construct_TouchLineEdit(QWidget *parent) {
  TouchLineEdit *lineEdit = reinterpret_cast<TouchLineEdit *>(calloc(1, 1024));
  TouchLineEdit__constructor(lineEdit, parent);
  return lineEdit;
}

TouchLabel *construct_TouchLabel(QWidget *parent) {
  TouchLabel *label = reinterpret_cast<TouchLabel *>(calloc(1, 1024));
  TouchLabel__constructor(label, parent, 0);
  return label;
}

N3ButtonLabel *construct_N3ButtonLabel(QWidget *parent) {
  N3ButtonLabel *button = reinterpret_cast<N3ButtonLabel *>(calloc(1, 1024));
  N3ButtonLabel__constructor(button, parent);
  return button;
}

KeyboardReceiver *construct_KeyboardReceiver(QLineEdit *lineEdit) {
  KeyboardReceiver *receiver = reinterpret_cast<KeyboardReceiver *>(calloc(1, 1024));
  KeyboardReceiver__constructor(receiver, lineEdit, false);
  return receiver;
}

// rebootDevice mirrors kpm's device.Reboot (§6): sync filesystems, give the
// kernel a moment to settle, then try the same command ladder. The settle runs
// on a timer so Nickel's UI thread never blocks.
void rebootDevice() {
  sync();
  QTimer::singleShot(2000, [] {
    if (QProcess::startDetached("/sbin/reboot", QStringList())) {
      return;
    }
    if (QProcess::startDetached("busybox", QStringList() << "reboot")) {
      return;
    }
    QProcess::startDetached("reboot", QStringList());
  });
}

// ---- entry point: "Package manager" row in the More menu (§3) -----------
//
// NickelMenu injects into the home/library "main" menu (the More tab on FW
// 4.23.15505+) by hooking AbstractNickelMenuController::createMenuTextItem and,
// when it sees the "Settings" text item being built, connecting to the menu's
// aboutToShow to append its own rows via createAction. We replicate exactly that
// for a single static row.

typedef QWidget MenuTextItem;
static MenuTextItem *(*AbstractNickelMenuController__createMenuTextItem)(void *_this, QMenu *menu, QString const &text,
                                                                         bool checkable, bool checked,
                                                                         QString const &objectName);
static QAction *(*AbstractNickelMenuController__createAction)(void *_this, QMenu *menu, QWidget *actionWidget,
                                                              bool closeOnTap, bool enabled, bool separatorAfter);

// injectRow appends the single "Package manager" row to a main/More menu, once
// per menu instance (guarded by a dynamic property), and opens BrowseDialog on
// tap. It no-ops safely if the createMenuTextItem/createAction symbols were not
// resolved on this firmware (§3: optional entry point).
static void injectRow(void *nmc, QMenu *menu) {
  if (!AbstractNickelMenuController__createMenuTextItem || !AbstractNickelMenuController__createAction) {
    nh_log("NickelKPM: menu symbols unavailable, cannot add entry row");
    return;
  }
  if (menu->property("nkpm_added").toBool()) {
    return; // aboutToShow fires on every open; add our row only once
  }
  menu->setProperty("nkpm_added", true);

  MenuTextItem *item =
      AbstractNickelMenuController__createMenuTextItem(nmc, menu, QStringLiteral("Package manager"), false, false,
                                                       QLatin1String(""));
  // closeOnTap=true, enabled=true, separatorAfter=true — mirrors NickelMenu's
  // own main-menu injection call.
  QAction *action = AbstractNickelMenuController__createAction(nmc, menu, item, true, true, true);
  QObject::connect(action, &QAction::triggered, menu, [](bool) {
    nh_log("NickelKPM: Package manager tapped");
    BrowseDialog::show();
  });
}

extern "C" __attribute__((visibility("default"))) MenuTextItem *
_nkpm_menu_hook(void *_this, QMenu *menu, QString const &label, bool checkable, bool checked,
                QString const &objectName) {
  // The main/More menu is identified exactly as NickelMenu does: the untranslated
  // "Settings" text item, non-checkable (§3).
  QString settings = QCoreApplication::translate("StatusBarMenuController", "Settings");
  if (label == settings && !checkable) {
    nh_log("NickelKPM: intercepting main/More menu (label=Settings)");
    void *nmc = _this;
    QObject::connect(menu, &QMenu::aboutToShow, menu, [nmc, menu]() { injectRow(nmc, menu); });
  }
  return AbstractNickelMenuController__createMenuTextItem(_this, menu, label, checkable, checked, objectName);
}

// ---- NickelHook registration --------------------------------------------

static struct nh_info NickelKPM = (struct nh_info){
    .name = "NickelKPM",
    .desc = "Graphical package browser for kpm",
    .uninstall_flag = "/mnt/onboard/kpm_ui_uninstall",
    // nullptr placeholder: the device GCC only accepts a gap-free designated-
    // initializer prefix ("non-trivial designated initializers not supported").
    .uninstall_xflag = nullptr,
    // If Nickel crashes within this window after load, NickelHook's failsafe
    // leaves libnkpm.so renamed aside so the next boot comes up without the mod
    // (no bootloop); a clean boot restores it once the window passes.
    .failsafe_delay = 15,
};

// clang-format off
static struct nh_hook NickelKPMHook[] = {
  // Entry point: hook createMenuTextItem to inject the "Package manager" row.
  // Optional so a symbol miss loads the mod with no entry point (§3.3).
  { .sym = "_ZN28AbstractNickelMenuController18createMenuTextItemEP5QMenuRK7QStringbbS4_", .sym_new = "_nkpm_menu_hook", .lib = "libnickel.so.1.0.0", .out = nh_symoutptr(AbstractNickelMenuController__createMenuTextItem), .desc = "More-menu entry row (NickelMenu main-menu technique)", .optional = true }, // libnickel 4.6+ (main menu → More tab on 4.23.15505+)
  {0},
};

static struct nh_dlsym NickelKPMDlsym[] = {
  // Entry-point helper (optional, pairs with the hook above, §3.3).
  { .name = "_ZN22AbstractMenuController12createActionEP5QMenuP7QWidgetbbb",                .out = nh_symoutptr(AbstractNickelMenuController__createAction), .desc = "More-menu entry row", .optional = true }, // libnickel 4.6+

  { .name = "_ZN20MainWindowController14sharedInstanceEv",                     .out = nh_symoutptr(MainWindowController__sharedInstance) },
  { .name = "_ZNK20MainWindowController11currentViewEv",                       .out = nh_symoutptr(MainWindowController__currentView) },
  { .name = "_ZN20MainWindowController8pushViewEP7QWidget",                    .out = nh_symoutptr(MainWindowController__pushView) },

  { .name = "_ZN25ConfirmationDialogFactory15showErrorDialogERK7QStringS2_",   .out = nh_symoutptr(ConfirmationDialogFactory__showErrorDialog) },
  { .name = "_ZN25ConfirmationDialogFactory21getConfirmationDialogEP7QWidget", .out = nh_symoutptr(ConfirmationDialogFactory__getConfirmationDialog) },
  { .name = "_ZN18ConfirmationDialog8setTitleERK7QString",                     .out = nh_symoutptr(ConfirmationDialog__setTitle) },
  { .name = "_ZN18ConfirmationDialog7setTextERK7QString",                      .out = nh_symoutptr(ConfirmationDialog__setText) },
  { .name = "_ZN18ConfirmationDialog19setAcceptButtonTextERK7QString",         .out = nh_symoutptr(ConfirmationDialog__setAcceptButtonText) },
  { .name = "_ZN18ConfirmationDialog19setRejectButtonTextERK7QString",         .out = nh_symoutptr(ConfirmationDialog__setRejectButtonText) },

  { .name = "_ZN23WirelessWorkflowManager14sharedInstanceEv",                  .out = nh_symoutptr(WirelessWorkflowManager__sharedInstance) },
  { .name = "_ZN23WirelessWorkflowManager20isInternetAccessibleEv",            .out = nh_symoutptr(WirelessWorkflowManager__isInternetAccessible) },
  { .name = "_ZN23WirelessWorkflowManager15connectWirelessEbb",                .out = nh_symoutptr(WirelessWorkflowManager__connectWireless) },
  { .name = "_ZN23WirelessWorkflowManager23connectWirelessSilentlyEv",         .out = nh_symoutptr(WirelessWorkflowManager__connectWirelessSilently) },
  { .name = "_ZN15WirelessManager14sharedInstanceEv",                          .out = nh_symoutptr(WirelessManager__sharedInstance) },

  { .name = "_ZN13TouchLineEditC1EP7QWidget",                                  .out = nh_symoutptr(TouchLineEdit__constructor) },
  { .name = "_ZN10TouchLabelC1EP7QWidget6QFlagsIN2Qt10WindowTypeEE",           .out = nh_symoutptr(TouchLabel__constructor) },
  { .name = "_ZN10TouchLabel18setHitStateEnabledEb",                           .out = nh_symoutptr(TouchLabel__setHitStateEnabled) },
  { .name = "_ZN13N3ButtonLabelC1EP7QWidget",                                  .out = nh_symoutptr(N3ButtonLabel__constructor) },

  { .name = "_ZN15N3DialogFactory9getDialogEP7QWidgetb",                       .out = nh_symoutptr(N3DialogFactory__getDialog) },
  { .name = "_ZN8N3Dialog8setTitleERK7QString",                                .out = nh_symoutptr(N3Dialog__setTitle) },
  { .name = "_ZN8N3Dialog13keyboardFrameEv",                                   .out = nh_symoutptr(N3Dialog__keyboardFrame) },
  { .name = "_ZN8N3Dialog12showKeyboardEv",                                    .out = nh_symoutptr(N3Dialog__showKeyboard) },
  { .name = "_ZN8N3Dialog12hideKeyboardEv",                                    .out = nh_symoutptr(N3Dialog__hideKeyboard) },

  { .name = "_ZN16KeyboardReceiverC1EP9QLineEditb",                            .out = nh_symoutptr(KeyboardReceiver__constructor) },
  { .name = "_ZN13KeyboardFrame14createKeyboardE14KeyboardScriptRK7QLocale",   .out = nh_symoutptr(KeyboardFrame__createKeyboard) },
  { .name = "_ZN24SearchKeyboardController11setReceiverEP16KeyboardReceiverb", .out = nh_symoutptr(SearchKeyboardController__setReceiver) },
  { .name = "_ZN24SearchKeyboardController9setGoTextERK7QString",              .out = nh_symoutptr(SearchKeyboardController__setGoText) },

  {0},
};
// clang-format on

NickelHook(.init = nullptr, .info = &NickelKPM, .hook = NickelKPMHook, .dlsym = NickelKPMDlsym, .uninstall = nullptr);
