#pragma once

// CONFIG.md §4 — rows for the ConfigDialog (contract: uicontract blocks 8-10).
// Two small tappable QFrames modelled on PackageRow (widgets/packagerow.{h,cc}):
//   ConfigFileRow  — one declared config file in the file-picker page
//                    (`kpm config list` payload: name/description/exists/…).
//   ConfigEntryRow — one entry of the open file in the entries page
//                    (`kpm config show` payload: section/key/line/value/…).
// As with PackageRow, Kobo touch input never reaches a bare QFrame, so the tap
// goes through a Nickel touch widget (an N3ButtonLabel's tapped() signal).

#include <QFrame>
#include <QJsonObject>

// ConfigFileRow renders one file from a `config list` payload and emits its
// 1-based index when its Open button is tapped.
class ConfigFileRow : public QFrame {
  Q_OBJECT

public:
  ConfigFileRow(QJsonObject file, int index, QWidget *parent = nullptr);

Q_SIGNALS:
  void selected(int index);

private Q_SLOTS:
  void openTapped();

private:
  int index;
};

// ConfigEntryRow renders one entry from a `config show` payload. `format` is the
// open file's format ("ini" | "text") and picks the layout: ini shows a bold
// "Section · Key" label with a right-aligned value (masked when sensitive); text
// shows the elided line content. It emits the full entry object on Edit so the
// dialog knows how to address the edit (by key or by line).
class ConfigEntryRow : public QFrame {
  Q_OBJECT

public:
  ConfigEntryRow(QJsonObject entry, QString format, QWidget *parent = nullptr);

  // entryData exposes the payload entry so the offscreen sim driver can locate a
  // specific row (main.cc --exercise-config).
  const QJsonObject &entryData() const { return entry; }

Q_SIGNALS:
  void selected(QJsonObject entry);

private Q_SLOTS:
  void editTapped();

private:
  QJsonObject entry;
};
