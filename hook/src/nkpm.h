#pragma once

// NICKEL-UI.md §1/§3 — NickelKPM shared Nickel symbol table.
//
// This is the subset of NickelHardcover's nickelhardcover.h (MIT) that the kpm
// UI needs: full-screen dialog + keyboard, confirmation/error dialogs, the
// Wi-Fi workflow, and the touch widgets used by the browse/detail views. The
// entry-point menu-injection symbols live privately in nkpm.cc (they are only
// used there). Copied and trimmed with attribution per NICKEL-UI.md §0.

#include <QDialog>
#include <QLabel>
#include <QLineEdit>
#include <QObject>
#include <QWidget>

typedef QObject MainWindowController;
extern MainWindowController *(*MainWindowController__sharedInstance)();
extern QWidget *(*MainWindowController__currentView)(MainWindowController *mwc);
extern QWidget *(*MainWindowController__pushView)(MainWindowController *mwc, QWidget *view);

// Optional (null when the firmware lacks them): status-bar chrome control, used
// to hide the home screen's persistent bars while a kpm dialog is open so the
// dialog's title bar and keyboard aren't drawn over (NICKELMENU-LAUNCH.md).
// All four confirmed present in FW 4.45.23697.
typedef QObject StatusBarController;
extern StatusBarController *(*MainWindowController__statusBarController)(MainWindowController *mwc);
extern void (*StatusBarController__hideStatusBar)(StatusBarController *sbc);
extern void (*StatusBarController__showStatusBar)(StatusBarController *sbc);
extern bool (*StatusBarController__isVisible)(StatusBarController *sbc);

typedef QDialog N3Dialog;
typedef QDialog ConfirmationDialog;
extern void (*ConfirmationDialogFactory__showErrorDialog)(QString const &title, QString const &body);
extern ConfirmationDialog *(*ConfirmationDialogFactory__getConfirmationDialog)(QWidget *parent);
extern void (*ConfirmationDialog__setTitle)(ConfirmationDialog *_this, QString const &);
extern void (*ConfirmationDialog__setText)(ConfirmationDialog *_this, QString const &);
extern void (*ConfirmationDialog__setAcceptButtonText)(ConfirmationDialog *_this, QString const &);
extern void (*ConfirmationDialog__setRejectButtonText)(ConfirmationDialog *_this, QString const &);

typedef QObject WirelessWorkflowManager;
extern WirelessWorkflowManager *(*WirelessWorkflowManager__sharedInstance)();
extern bool (*WirelessWorkflowManager__isInternetAccessible)(WirelessWorkflowManager *);
extern void (*WirelessWorkflowManager__connectWirelessSilently)(WirelessWorkflowManager *);
extern void (*WirelessWorkflowManager__connectWireless)(WirelessWorkflowManager *_this, bool, bool);

typedef QObject WirelessManager;
extern WirelessManager *(*WirelessManager__sharedInstance)();

typedef QWidget KeyboardReceiver;
KeyboardReceiver *construct_KeyboardReceiver(QLineEdit *lineEdit);

typedef QWidget KeyboardFrame;
typedef QObject SearchKeyboardController;
extern SearchKeyboardController *(*KeyboardFrame__createKeyboard)(KeyboardFrame *__this, int keyboardScript,
                                                                  QLocale locale);
extern void (*SearchKeyboardController__setReceiver)(SearchKeyboardController *__this, KeyboardReceiver *receiver,
                                                     bool idk);
extern void (*SearchKeyboardController__setGoText)(SearchKeyboardController *__this, QString const &text);

extern N3Dialog *(*N3DialogFactory__getDialog)(QWidget *content, bool idk);
extern void (*N3Dialog__setTitle)(N3Dialog *__this, QString const &);
// Optional (null when the firmware lacks it): asks Nickel to present the dialog
// as a full view, which suppresses the home screen's status/nav chrome —
// NickelHardcover's JournalDialog uses it (NICKELMENU-LAUNCH.md).
extern void (*N3Dialog__enableFullViewMode)(N3Dialog *__this);
// Optional: shows the title-bar back button. DetailDialog wires its backTapped()
// to return to the browse list; the close (X) button closes the whole UI.
extern void (*N3Dialog__enableBackButton)(N3Dialog *__this, bool enable);
extern KeyboardFrame *(*N3Dialog__keyboardFrame)(N3Dialog *__this);
extern void (*N3Dialog__showKeyboard)(N3Dialog *__this);
extern void (*N3Dialog__hideKeyboard)(N3Dialog *__this);

typedef QLineEdit TouchLineEdit;
TouchLineEdit *construct_TouchLineEdit(QWidget *parent);

typedef QLabel TouchLabel;
TouchLabel *construct_TouchLabel(QWidget *parent);
extern void (*TouchLabel__setHitStateEnabled)(TouchLabel *_this, bool enabled);

typedef TouchLabel N3ButtonLabel;
N3ButtonLabel *construct_N3ButtonLabel(QWidget *parent);

// rebootDevice reboots the Kobo, trying the same command ladder as kpm's own
// device.Reboot (NICKEL-UI.md §6 action step 5): /sbin/reboot, busybox reboot,
// reboot.
void rebootDevice();
