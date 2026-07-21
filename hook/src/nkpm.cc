// NICKEL-UI.md §1/§3, NICKELMENU-LAUNCH.md — NickelKPM hook and Nickel symbols.
//
// Structure and the Nickel symbol set are adapted from NickelHardcover's
// nickelhardcover.cc (MIT, https://codeberg.org/StrayRose/NickelHardcover).
// The UI is launched from a NickelMenu "kpm - Package manager" item (which runs
// `kpm ui`) rather than by injecting a menu row: FW 4.23.15505+ moved the "main"
// menu off the More tab, so the old createMenuTextItem/"Settings" anchor no
// longer fires (verified on 4.45.23697). The hook is now init-only — it watches
// Files::ui_trigger and opens BrowseDialog — and hooks no Nickel functions.

#include <QFile>
#include <QFileSystemWatcher>
#include <QProcess>
#include <QStringList>
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
StatusBarController *(*MainWindowController__statusBarController)(MainWindowController *mwc);
void (*StatusBarController__hideStatusBar)(StatusBarController *sbc);
void (*StatusBarController__showStatusBar)(StatusBarController *sbc);
bool (*StatusBarController__isVisible)(StatusBarController *sbc);

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
void (*N3Dialog__enableFullViewMode)(N3Dialog *__this);
void (*N3Dialog__enableBackButton)(N3Dialog *__this, bool enable);
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

// ---- entry point: NickelMenu-launched UI (NICKELMENU-LAUNCH.md) ----------
//
// The 4.23-era "inject a row when the More tab builds its Settings item"
// technique is dead on FW 4.23.15505+ (Nickel moved that menu to a nav-bar
// button), so the UI is now launched from a NickelMenu item that runs `kpm ui`,
// which writes to Files::ui_trigger. On init we watch that tmpfs file and open
// the browser when it changes. The watcher and its handler run on Nickel's main
// (UI) thread — init is called there — so creating the dialog is safe.

static void onUITrigger() {
  static bool pending = false;
  if (pending) {
    return; // debounce: inotify can deliver a single write as several events
  }
  pending = true;
  QTimer::singleShot(400, [] { pending = false; });
  nh_log("NickelKPM: UI trigger fired");
  BrowseDialog::show(); // re-entrancy-guarded (only one browser at a time)
}

static int nkpm_init() {
  // Ensure the trigger file exists so the watcher has a path to watch (tmpfs is
  // empty at boot); a plain create is enough, `kpm ui` truncates+writes it.
  { QFile f(QString::fromUtf8(Files::ui_trigger)); f.open(QIODevice::WriteOnly); }

  static QFileSystemWatcher *watcher = new QFileSystemWatcher(QStringList() << QString::fromUtf8(Files::ui_trigger));
  QObject::connect(watcher, &QFileSystemWatcher::fileChanged, watcher, [](QString const &path) {
    // Re-arm: QFileSystemWatcher drops the watch if the file was replaced
    // (delete+create). `kpm ui` truncates in place, but guard regardless.
    if (!watcher->files().contains(path)) {
      QFile f(path);
      f.open(QIODevice::WriteOnly);
      f.close();
      watcher->addPath(path);
    }
    onUITrigger();
  });
  nh_log("NickelKPM: watching %s for UI launch", Files::ui_trigger);
  return 0;
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
// No Nickel functions are hooked: the UI is launched from a NickelMenu item via
// Files::ui_trigger (NICKELMENU-LAUNCH.md), not by injecting a menu row.
static struct nh_hook NickelKPMHook[] = {
  {0},
};

static struct nh_dlsym NickelKPMDlsym[] = {
  { .name = "_ZN20MainWindowController14sharedInstanceEv",                     .out = nh_symoutptr(MainWindowController__sharedInstance) },
  { .name = "_ZNK20MainWindowController11currentViewEv",                       .out = nh_symoutptr(MainWindowController__currentView) },
  { .name = "_ZN20MainWindowController8pushViewEP7QWidget",                    .out = nh_symoutptr(MainWindowController__pushView) },
  // Optional chrome-control set (dialog.cc hides the bars while a dialog is
  // open); absence falls back to the geometry-inset workaround, never a
  // load failure. All present in FW 4.45.23697.
  { .name = "_ZNK20MainWindowController19statusBarControllerEv",               .out = nh_symoutptr(MainWindowController__statusBarController), .desc = "MainWindowController::statusBarController()", .optional = true },
  { .name = "_ZN19StatusBarController13hideStatusBarEv",                       .out = nh_symoutptr(StatusBarController__hideStatusBar), .desc = "StatusBarController::hideStatusBar()", .optional = true },
  { .name = "_ZN19StatusBarController13showStatusBarEv",                       .out = nh_symoutptr(StatusBarController__showStatusBar), .desc = "StatusBarController::showStatusBar()", .optional = true },
  { .name = "_ZN19StatusBarController9isVisibleEv",                            .out = nh_symoutptr(StatusBarController__isVisible), .desc = "StatusBarController::isVisible()", .optional = true },

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
  // Optional so an absence can't stop the whole mod loading; call sites null-check.
  { .name = "_ZN8N3Dialog18enableFullViewModeEv",                              .out = nh_symoutptr(N3Dialog__enableFullViewMode), .desc = "N3Dialog::enableFullViewMode()", .optional = true },
  { .name = "_ZN8N3Dialog16enableBackButtonEb",                                .out = nh_symoutptr(N3Dialog__enableBackButton), .desc = "N3Dialog::enableBackButton(bool)", .optional = true },
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

NickelHook(.init = nkpm_init, .info = &NickelKPM, .hook = NickelKPMHook, .dlsym = NickelKPMDlsym, .uninstall = nullptr);
