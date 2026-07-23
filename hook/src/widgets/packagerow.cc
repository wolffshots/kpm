// NICKEL-UI.md §5 — PackageRow. Adapted from NickelHardcover's bookrow.cc (MIT)
// per §0; simplified to a text-only, tappable row (no cover art / buttons).

#include <QHBoxLayout>
#include <QJsonArray>
#include <QVBoxLayout>

#include "../nkpm.h"
#include "elidedlabel.h"
#include "label.h"
#include "packagerow.h"

QString PackageRow::badgeText(const QJsonObject &json) {
  // §A (post-install manifest verification): a non-empty missing_files array is
  // the TOP-priority badge — the package promoted to installed but one or more
  // of its manifest members never landed (an rcS failure or foreign tgz). It
  // outranks staged/installed/update: those describe intent, this describes a
  // broken install. missing_files is null/absent when the package verified clean
  // (search --json, server-computed), so toArray() yields empty and this is skipped.
  if (!json.value("missing_files").toArray().isEmpty()) {
    return QStringLiteral("files missing");
  }
  // §5 state table. installed is JSON null (→ empty string) when not installed;
  // latest/update are merged in from `kpm check --json` (absent until a check).
  if (json.value("staged").toBool()) {
    return QStringLiteral("● reboot pending"); // ●
  }
  // Self-update enrolment (kpm-self-enrol-plan §5): the kpm row's self-update is
  // not wired up. Ranks below files-missing/staged (those describe a broken or
  // pending install) but above the normal not-installed/update/current lines — an
  // un-adopted kpm otherwise renders a healthy "0.7.0 ✓". Gated on the
  // server-computed is_self && !self_configured; both false for every non-kpm row.
  if (json.value("is_self").toBool() && !json.value("self_configured").toBool()) {
    return QStringLiteral("self-update off");
  }
  QString installed = json.value("installed").toString();
  if (installed.isEmpty()) {
    return QStringLiteral("not installed");
  }
  QString latest = json.value("latest").toString();
  if (json.value("update").toBool() && !latest.isEmpty()) {
    return installed + QStringLiteral(" → ") + latest + QStringLiteral(" ↑"); // → ↑
  }
  return installed + QStringLiteral(" ✓"); // ✓
}

PackageRow::PackageRow(QJsonObject json, QWidget *parent)
    : QFrame(parent), id(json.value("id").toString()) {
  setStyleSheet(R"(
    PackageRow {
      border-top: 1px solid #666666;
      padding: 14px;
    }
    PackageRow[noBorder=true] {
      border-top-width: 0;
    }
  )");

  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(0, 0, 0, 0);
  layout->setSpacing(2);

  QHBoxLayout *top = new QHBoxLayout();
  top->setContentsMargins(0, 0, 0, 0);
  layout->addLayout(top);

  QString name = json.value("name").toString();
  if (name.isEmpty()) {
    name = id;
  }
  Label *nameLabel = new Label(Label::Large, name);
  nameLabel->setStyleSheet(nameLabel->styleSheet() + "\nLabel { font-weight: bold; }");
  top->addWidget(nameLabel, 1);

  Label *badge = new Label(Label::Small, badgeText(json));
  badge->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
  top->addWidget(badge, 0, Qt::AlignRight);

  // A Nickel touch button opens the detail dialog: Kobo does not deliver a
  // plain QFrame a mouseReleaseEvent, so the tap must go through tapped().
  N3ButtonLabel *manage = construct_N3ButtonLabel(this);
  manage->setText("Manage");
  top->addWidget(manage, 0, Qt::AlignRight);
  QObject::connect(manage, SIGNAL(tapped(bool)), this, SLOT(manageTapped()));

  QString subtitle = json.value("description").toString();
  if (subtitle.isEmpty()) {
    subtitle = json.value("source").toString();
  }
  layout->addWidget(new ElidedLabel(Label::Small, subtitle));
}

void PackageRow::manageTapped() { selected(id); }
