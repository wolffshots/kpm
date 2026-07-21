#pragma once

// NICKEL-UI.md §6 — per-package detail view and action flow (Install / Update /
// Uninstall, with confirm + busy + reboot-prompt). Built on the Dialog base.

#include <QJsonObject>

#include "kpmprocess.h"
#include "widgets/dialog.h"

class QLabel;

class DetailDialog : public Dialog {
  Q_OBJECT

public:
  // show opens the detail view for a package payload object (a single element of
  // `kpm search --json`, possibly merged with check data).
  static DetailDialog *show(QJsonObject package);

Q_SIGNALS:
  // changed is emitted after a successful mutation so the browse list reloads.
  void changed();

public Q_SLOTS:
  void install();
  void update();
  void uninstall();

  void onResponse(int exitCode, QJsonObject payload);
  void onInstallRegistered(int exitCode, QJsonObject payload);
  void onFailure(QString reason);

private:
  DetailDialog(QJsonObject package);

  void run(KpmProcess *proc);      // shared action runner (§6 steps 2-3)
  void setBusy(bool busy);
  void promptReboot();

  QJsonObject pkg;
  QString id;
  QLabel *statusLabel = nullptr;
  QList<QWidget *> buttons;
};
