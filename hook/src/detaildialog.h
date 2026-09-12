#pragma once

// NICKEL-UI.md §6 — per-package detail view and action flow (Install / Update /
// Uninstall, with confirm + busy + reboot-prompt). Built on the Dialog base.

#include <QHash>
#include <QJsonObject>

#include "kpmprocess.h"
#include "widgets/dialog.h"

class QHBoxLayout;
class QLabel;

// The two predicates every string about an install shape is worded from, in ONE
// place because BrowseDialog and DetailDialog both ask (fonts plan §3.9). Retyping
// them per file is the drift class the plan warns about for the Go side: a page can
// then contradict the prompt that follows it, with both looking locally correct.
//
// isLiveMethod: the files land while the command runs, instead of being staged in a
// KoboRoot.tgz the firmware unpacks at the next boot. An ABSENT install_method is a
// kpm that predates the field (APP-TREE.md §11.1), where everything was a tgz — so
// this asks for a known non-tgz method rather than for "not tgz".
inline bool isLiveMethod(const QJsonObject &pkg) {
  QString method = pkg.value("install_method").toString();
  return !method.isEmpty() && method != QStringLiteral("tgz");
}

// isContentKind: the files are written immediately AND Nickel only reads them after
// a restart (D6), which is what separates a font from an ordinary live package.
// "dictionary" is deliberately absent: nothing emits that kind in 0.12.0 (D8), so
// including it would be a dead branch answering with font wording. The dictionary
// release adds it here, with its own sentence, after T4. The tab filter does not go
// through this — it compares the kind string directly, so the Dictionaries tab
// still filters like the other two.
inline bool isContentKind(const QJsonObject &pkg) {
  return pkg.value("kind").toString() == QStringLiteral("font");
}

// contentRebootText is the post-action prompt for a content package (fonts plan
// §3.9). Its files are written while the command runs, but the firmware only picks
// them up after a restart, so neither "Changes staged" (nothing is staged) nor
// silence (the old font keeps rendering) is true. Shared with
// BrowseDialog::rebootPromptText, which words the same fact for a whole run.
inline QString contentRebootText() {
  return QStringLiteral("Font changes take effect after a reboot. Reboot now?");
}

class DetailDialog : public Dialog {
  Q_OBJECT

public:
  // show opens the detail view for a package payload object (a single element of
  // `kpm search --json`, possibly merged with check data). packages is the whole
  // search list keyed by id, so this view can describe a package it does not itself
  // hold — the conflict route's blocker, whose NAME goes on the button and whose
  // installed/staged state words the advisory (INTEGRITY.md §4).
  static DetailDialog *show(QJsonObject package, const QHash<QString, QJsonObject> &packages);

  // closeView dismisses this detail view. Public because the conflict route's owner
  // (BrowseDialog) decides when it goes: the view must survive a route that found
  // nothing, or the user loses the page explaining the conflict along with it.
  void closeView();
  // routeFailed re-enables this page after an openPackage() the owner could not
  // satisfy — the counterpart to the setBusy(true) openBlocker() takes on tap.
  void routeFailed();

Q_SIGNALS:
  // changed is emitted after a successful mutation so the browse list reloads.
  void changed();
  // closeRequested is emitted when the title-bar close (X) is tapped, asking the
  // browse dialog to close too so the whole UI dismisses (the back button, by
  // contrast, only closes this detail view — see the constructor).
  void closeRequested();
  // selfEnrolled is emitted after a successful "Enable self-update" when the user
  // opts into the post-enrol check: BrowseDialog binds it to loadSearch(true)
  // (search + check) so the kpm row repaints with its real update badge
  // (kpm-self-enrol-plan §5). Distinct from changed(), which reloads without a check.
  void selfEnrolled();
  // openPackage asks the browse dialog to open ANOTHER package's detail view. It
  // is emitted by the conflict route (INTEGRITY.md §4): a package whose install is
  // refused sends the user to the blocking package, where Uninstall lives. Browse
  // owns the lookup because it owns the package list — see openConflictPackage.
  void openPackage(QString id);

public Q_SLOTS:
  void install();
  void update();
  void uninstall();
  void openConfig();        // Settings → ConfigDialog (CONFIG.md §4)
  void enableSelfUpdate();  // Enable self-update → adopt-self (kpm-self-enrol-plan §5)
  void openBlocker();       // "Manage <id>" → openPackage for a conflict blocker (INTEGRITY.md §4)
  void dropRegistration();  // "Drop registration" → kpm remove (INTEGRITY.md §4.3's orphaned def)
  void promptReboot();      // also a button on the staged page — see the constructor

  void onResponse(int exitCode, QJsonObject payload);
  void onInstallRegistered(int exitCode, QJsonObject payload);
  void onSelfEnrolled(int exitCode, QJsonObject payload);
  // onRegistered receives `kpm list --json` and offers "Drop registration" when this
  // package turns out to be registered but not installed (INTEGRITY.md §4.3).
  void onRegistered(int exitCode, QJsonObject payload);
  void onFailure(QString reason);

private:
  DetailDialog(QJsonObject package, const QHash<QString, QJsonObject> &packages);

  // busyLabel is the status text shown while the action runs; empty means the
  // generic "Working…" (APP-TREE.md §11.3 item 3).
  void run(KpmProcess *proc, const QString &busyLabel = QString()); // shared action runner (§6 steps 2-3)
  void setBusy(bool busy, const QString &busyLabel = QString());
  void addActionButton(const QString &text, const char *slot);
  void addBlockerButton(const QString &blocker);
  // actionLabel decorates an Install/Update button label with the "(no reboot)"
  // suffix; installBusyLabel picks that action's busy text. Both read `live` and
  // `content` and nothing else (APP-TREE.md §11.3 items 2-3, fonts plan §3.9).
  QString actionLabel(const QString &verb) const;
  QString installBusyLabel() const;
  QString displayName(const QString &packageId) const;
  // blockerStagedOnly reports whether a conflict blocker is present only as a
  // STAGED change (files landing at the next reboot) rather than installed — the
  // difference the advisory copy has to carry (INTEGRITY.md §4.3).
  bool blockerStagedOnly(const QString &packageId) const;

  QJsonObject pkg;
  QString id;
  // The two facts every string on this page is worded from (fonts plan §3.9).
  //
  // live: the payload's install_method is something other than "tgz" (APP-TREE.md
  // §11.1) — the files land while the command runs, rather than being staged in a
  // KoboRoot.tgz the firmware unpacks at the next boot.
  // content: the payload's `kind` is a font or a dictionary. Those are live too,
  // but Nickel only reads the new files after a restart (D6), so they take the
  // plain action verb and their own reboot sentence.
  //
  // A kpm that predates either field omits it: install_method reads as tgz and
  // `kind` as mod, i.e. every string on this page is the pre-0.11.0 one.
  bool live = false;
  bool content = false;
  QHash<QString, QJsonObject> packageIndex;
  QLabel *statusLabel = nullptr;
  QHBoxLayout *actionsLayout = nullptr; // buttons can still be added after the probe
  QList<QWidget *> buttons;
  bool busyNow = false; // so a button added after setBusy() does not arrive live
};
