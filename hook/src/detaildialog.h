#pragma once

// NICKEL-UI.md §6 — per-package detail view and action flow (Install / Update /
// Uninstall, with confirm + busy + reboot-prompt). Built on the Dialog base.

#include <QHash>
#include <QJsonObject>

#include "kpmprocess.h"
#include "widgets/dialog.h"

class QHBoxLayout;
class QLabel;

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
  // actionLabel decorates an Install/Update button label with the app-tree
  // "(no reboot)" suffix; installBusyLabel picks that action's busy text. Both
  // read `appTree` and nothing else (APP-TREE.md §11.3 items 2-3).
  QString actionLabel(const QString &verb) const;
  QString installBusyLabel() const;
  QString displayName(const QString &packageId) const;
  // blockerStagedOnly reports whether a conflict blocker is present only as a
  // STAGED change (files landing at the next reboot) rather than installed — the
  // difference the advisory copy has to carry (INTEGRITY.md §4.3).
  bool blockerStagedOnly(const QString &packageId) const;

  QJsonObject pkg;
  QString id;
  // appTree is the payload's install_method == "app-tree" (APP-TREE.md §11.1): this
  // package is extracted straight into .adds/<name>/ and is live the moment the
  // command returns, rather than staged in a KoboRoot.tgz for the next boot. Absent
  // on a pre-0.11.0 kpm, which reads as false — i.e. today's wording everywhere.
  bool appTree = false;
  QHash<QString, QJsonObject> packageIndex;
  QLabel *statusLabel = nullptr;
  QHBoxLayout *actionsLayout = nullptr; // buttons can still be added after the probe
  QList<QWidget *> buttons;
  bool busyNow = false; // so a button added after setBusy() does not arrive live
};
