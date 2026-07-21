#pragma once

// Adapted from NickelHardcover hook/src/widgets/label.h (MIT,
// https://codeberg.org/StrayRose/NickelHardcover) per NICKEL-UI.md §0.

#include <QLabel>

class Label : public QLabel {
  Q_OBJECT
  Q_PROPERTY(QString textSize MEMBER textSize)

public:
  static const QString Avenir;
  static const QString ExtraSmall;
  static const QString Small;
  static const QString Medium;
  static const QString Large;
  static const QString ExtraLarge;
  static const QString Stylesheet;

  explicit Label(const QString &textSize, const QString &text, QWidget *parent = nullptr);

private:
  QString textSize;
};
