#pragma once

// NICKEL-UI.md §5 — BrowseDialog: the full-screen package list opened from the
// More-menu entry row. Search box + client-side filter, paged PackageRows,
// staged/stale banners, and Refresh / Sync / Update-all actions. Built on the Dialog
// base (with NH's ReadingView auto-close removed).

#include <QHash>
#include <QJsonArray>
#include <QJsonObject>
#include <QList>

#include "widgets/dialog.h"
#include "widgets/pagedstack.h"

class DetailDialog;
class QVBoxLayout;

class BrowseDialog : public Dialog {
  Q_OBJECT

public:
  static void show();

  void commit() override; // keyboard "Search" / go — re-filters in place

public Q_SLOTS:
  void requestPage(int index);
  void onFirstLayout();

  void onSearch(int exitCode, QJsonObject payload);
  void onRefreshDone(int exitCode, QJsonObject payload);
  void onCheckDone(int exitCode, QJsonObject payload);
  void onActionDone(int exitCode, QJsonObject payload);
  void onProcessFailure(QString reason);

  void refresh();
  void sync();
  void updateAll();
  void rebootConfirm();
  void openDetail(QString id);

  // The tab bar (the fonts plan §3.8). N3ButtonLabel is an opaque Nickel class, so
  // its tapped(bool) signal is reachable only through the string-based SIGNAL/SLOT
  // macros, which cannot bind a lambda — hence one named slot per tab rather than
  // one slot carrying its kind.
  void showModsTab();
  void showFontsTab();
  void showDictsTab();

private:
  BrowseDialog();

  // openConflictPackage opens a package a conflict advisory routed to, reporting a
  // miss rather than no-oping, and closing the source view only when the route
  // actually went somewhere (INTEGRITY.md §4 — see browsedialog.cc).
  void openConflictPackage(DetailDialog *from, const QString &id);
  void loadSearch(bool thenCheck);
  // allowInPlace enables flash-reduction win 4 (repaint only the changed rows).
  // Passed true only by the callers whose data is a fresh kpm payload.
  void render(bool allowInPlace);
  bool repaintRowsInPlace(const QList<QJsonObject> &previous);
  void mergeCheck(const QJsonArray &checkPackages);
  bool registriesStale() const;
  void setActionsEnabled(bool enabled);
  QList<QJsonObject> filteredPackages() const;
  // packageIndex maps id -> package object over the whole list, so a detail view can
  // describe a package it does not hold: the blocker's display name on the button,
  // and its installed/staged state in the advisory (INTEGRITY.md §4).
  QHash<QString, QJsonObject> packageIndex() const;
  // skipNotices renders a mutation payload's `skipped` array (INTEGRITY.md §6.4) as
  // one human line per package.
  QStringList skipNotices(const QJsonObject &payload) const;
  // rebootPromptText words the post-action reboot prompt for the run that just
  // finished — the mixed app-tree + tgz case needs more than "Changes staged"
  // (APP-TREE.md §6.4). showRebootPrompt raises it; rebootConfirm() stays the
  // no-argument slot the staged banner's button connects to by name.
  QString rebootPromptText(const QJsonObject &payload) const;
  void showRebootPrompt(const QString &text);
  // selectTab switches which `kind` the list shows; styleTabs marks the active one
  // (§3.8). emptyStateText words the status line for a tab with no rows.
  void selectTab(const QString &kind);
  void styleTabs();
  QString emptyStateText() const;
  // addPageOneChrome draws the global page-1 banners (staged, skips, stale hint)
  // into a page being built — including the page an empty tab shows (D7).
  void addPageOneChrome(QWidget *box, QVBoxLayout *v);

  PagedStack *pages = nullptr;
  TouchLineEdit *lineEdit = nullptr;
  N3ButtonLabel *modsTab = nullptr;
  N3ButtonLabel *fontsTab = nullptr;
  N3ButtonLabel *dictsTab = nullptr;
  N3ButtonLabel *refreshButton = nullptr;
  N3ButtonLabel *syncButton = nullptr;
  N3ButtonLabel *updateAllButton = nullptr;

  QJsonArray allPackages;
  QJsonObject stagedSummary;
  QJsonArray registries;
  QList<QJsonObject> filtered;
  QString filter;
  // The `kind` (search --json) the list is filtered to — the active tab. "mod" is
  // the field's own default for every package with no content destination, so it is
  // also where a payload with no `kind` at all lands.
  QString activeKind = QStringLiteral("mod");
  // Packages the last action deliberately did not act on (INTEGRITY.md §6.4's
  // `skipped`). Rendered on page 1 until the next action replaces or clears it:
  // a skip is neither a failure nor a success, and silence is the one thing it
  // must not be — "Update all" succeeding while a package stays behind is exactly
  // what a permanent conflict refusal looks like from the UI.
  QStringList skipNotes;

  bool kpmMissing = false;
  bool dataReady = false;
  bool laidOut = false;
  bool pendingCheck = false;
  // The page-1 banner/hint states the last render actually drew (win 4): a flip in
  // either can only be applied by rebuilding page 1, so the in-place path checks
  // them. Recorded in render(), because onSearch overwrites their inputs first.
  bool renderedStagedBanner = false;
  bool renderedStaleHint = false;
  bool renderedSkipNotice = false;
};
