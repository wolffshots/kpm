#pragma once

// NICKEL-UI.md §5 — BrowseDialog: the full-screen package list opened from the
// More-menu entry row. Search box + client-side filter, paged PackageRows,
// staged/stale banners, and Refresh / Update-all actions. Built on the Dialog
// base (with NH's ReadingView auto-close removed).

#include <QJsonArray>
#include <QJsonObject>
#include <QList>

#include "widgets/dialog.h"
#include "widgets/pagedstack.h"

class DetailDialog;

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
  void updateAll();
  void rebootConfirm();
  void openDetail(QString id);

private:
  BrowseDialog();

  void loadSearch(bool thenCheck);
  void render();
  void mergeCheck(const QJsonArray &checkPackages);
  bool registriesStale() const;
  void setActionsEnabled(bool enabled);
  QList<QJsonObject> filteredPackages() const;

  PagedStack *pages = nullptr;
  TouchLineEdit *lineEdit = nullptr;
  N3ButtonLabel *refreshButton = nullptr;
  N3ButtonLabel *updateAllButton = nullptr;

  QJsonArray allPackages;
  QJsonObject stagedSummary;
  QJsonArray registries;
  QList<QJsonObject> filtered;
  QString filter;

  bool kpmMissing = false;
  bool dataReady = false;
  bool laidOut = false;
  bool pendingCheck = false;
};
