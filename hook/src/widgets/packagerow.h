#pragma once

// NICKEL-UI.md §5 — one package in the browse list. Structure adapted from
// NickelHardcover hook/src/search/bookrow.{h,cc} (MIT) per §0: name (bold),
// a right-aligned version/state badge, and an elided description (falling back
// to the source URL). The whole row is tappable and emits selected(id).

#include <QFrame>
#include <QJsonObject>

class PackageRow : public QFrame {
  Q_OBJECT

public:
  PackageRow(QJsonObject json, QWidget *parent = nullptr);

  // badgeText renders the state badge for a package payload object
  // (JSON-OUTPUT.md §2.1 merged with check data), per the §5 table.
  static QString badgeText(const QJsonObject &json);

Q_SIGNALS:
  void selected(QString id);

protected:
  void mouseReleaseEvent(QMouseEvent *event) override;

private:
  QString id;
};
