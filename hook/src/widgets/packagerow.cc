// NICKEL-UI.md §5 — PackageRow. Adapted from NickelHardcover's bookrow.cc (MIT)
// per §0; simplified to a text-only, tappable row (no cover art / buttons).

#include <QHBoxLayout>
#include <QMouseEvent>
#include <QVBoxLayout>

#include "elidedlabel.h"
#include "label.h"
#include "packagerow.h"

QString PackageRow::badgeText(const QJsonObject &json) {
  // §5 state table. installed is JSON null (→ empty string) when not installed;
  // latest/update are merged in from `kpm check --json` (absent until a check).
  if (json.value("staged").toBool()) {
    return QStringLiteral("● reboot pending"); // ●
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

  QString subtitle = json.value("description").toString();
  if (subtitle.isEmpty()) {
    subtitle = json.value("source").toString();
  }
  layout->addWidget(new ElidedLabel(Label::Small, subtitle));
}

void PackageRow::mouseReleaseEvent(QMouseEvent *event) {
  QFrame::mouseReleaseEvent(event);
  if (event->button() == Qt::LeftButton && rect().contains(event->pos())) {
    selected(id);
  }
}
