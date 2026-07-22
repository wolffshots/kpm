#pragma once

// CONFIG.md §4 — ConfigDialog: view and edit an installed package's declared
// config files on-device. Opened from DetailDialog's Settings button (shown only
// when the package is installed and `search --json` reports has_config). Renders
// the JSON from `kpm config list` / `kpm config show` and writes edits back via
// `kpm config set`. Contract (field names/types): uicontract_test.go blocks 8-10.
//
// Navigation mirrors DetailDialog: pushed as its own full-screen view over the
// detail view, the title-bar back button returns to detail, and the close X
// chains the whole package-manager stack closed (closeRequested). Built on the
// Dialog base and its buildKeyboardFrame() single-line editor.

#include <QJsonArray>
#include <QJsonObject>

#include "kpmprocess.h"
#include "widgets/dialog.h"
#include "widgets/pagedstack.h"

class Label;

class ConfigDialog : public Dialog {
  Q_OBJECT

public:
  // show opens the config editor for an installed package (id + display name).
  static ConfigDialog *show(const QString &id, const QString &name);

  void commit() override; // keyboard "Save" / go — writes the edited value

Q_SIGNALS:
  // closeRequested asks the parent (DetailDialog) to close too, so the whole UI
  // dismisses on the close X — the back button, by contrast, only pops this view.
  void closeRequested();

public Q_SLOTS:
  void requestPage(int index);
  void onFirstLayout();

  void onConfigList(int exitCode, QJsonObject payload);
  void onConfigShow(int exitCode, QJsonObject payload);
  void onSetDone(int exitCode, QJsonObject payload);
  void onProcessFailure(QString reason);

  void openFile(int index);            // file picker → open one file
  void beginEdit(QJsonObject entry);   // entry row → edit its value
  void beginAppend();                  // "Add line" → append a new text line
  void createFromTemplate();           // "Create from example" → seed from template
  void deleteCurrentLine();            // edit-mode "Delete line"
  void maybePromptReboot();            // on leaving, if a reboot file changed

private:
  ConfigDialog(const QString &id, const QString &name);

  void loadList();
  void render();
  void showEntries(const QJsonObject &fileObj, const QJsonArray &entries);
  void endEdit();
  void setActionsEnabled(bool enabled);

  QString id;
  QString name;

  PagedStack *pages = nullptr;
  Label *header = nullptr;
  Label *hint = nullptr;

  // Edit footer: a single-line editor (with the Nickel keyboard built once) plus
  // the text-only Add/Delete affordances.
  QWidget *editRow = nullptr;
  Label *editPrompt = nullptr;
  TouchLineEdit *editLineEdit = nullptr;
  N3ButtonLabel *deleteButton = nullptr;
  N3ButtonLabel *addLineButton = nullptr;
  // "Create from example": shown when the open file is missing but declares a
  // seed template (CONFIG.md §3.x). Tapping it runs `kpm config init`.
  N3ButtonLabel *createButton = nullptr;

  // Data. In FileList mode `configs` drives the picker; once a file is open,
  // `entries` drives the rows and the current* fields describe it.
  enum Mode { FileList, Entries };
  Mode mode = FileList;
  QJsonArray configs;
  QJsonArray entries;
  QString currentSelector; // the file's declared name, the `config set` selector
  QString currentFormat;   // "ini" | "text"
  QString currentReload;   // "auto" | "reboot"
  bool currentExists = false;      // the open file exists on disk
  bool currentHasTemplate = false; // the open file declares a seed template

  // Edit state: what commit() will write.
  enum EditMode { EditNone, EditIniKey, EditTextLine, EditTextAppend };
  EditMode editMode = EditNone;
  QString editSection; // ini
  QString editKey;     // ini
  int editLine = 0;    // text

  bool rebootPending = false; // a reboot-reload file changed this session
  bool dataReady = false;
  bool laidOut = false;
};
