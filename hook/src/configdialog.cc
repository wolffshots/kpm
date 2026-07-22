// CONFIG.md §4 — ConfigDialog. Renders `kpm config list`/`show` and writes edits
// via `kpm config set` (contract: uicontract_test.go blocks 8-10). Layout,
// pagination and keyboard patterns are the BrowseDialog/DetailDialog ones.

#include <QHBoxLayout>
#include <QVBoxLayout>

#include <NickelHook.h>

#include "configdialog.h"
#include "files.h"
#include "nkpm.h"
#include "widgets/configrow.h"
#include "widgets/label.h"

ConfigDialog *ConfigDialog::show(const QString &id, const QString &name) { return new ConfigDialog(id, name); }

ConfigDialog::ConfigDialog(const QString &pkgId, const QString &pkgName)
    : Dialog(pkgName.isEmpty() ? pkgId : pkgName), id(pkgId), name(pkgName) {
  if (QWidget *parent = parentWidget()) {
    setFixedSize(parent->size());
  }

  // Title-bar buttons, mirroring DetailDialog: the back arrow pops this view back
  // to the detail view; the close X chains the whole package-manager stack shut
  // (base Dialog deletes this view on closeTapped; closeRequested closes detail
  // underneath). Either exit offers the reboot prompt if a reboot file changed.
  if (N3Dialog__enableBackButton) {
    N3Dialog__enableBackButton(dialog, true);
  }
  QObject::connect(dialog, SIGNAL(backTapped()), this, SLOT(maybePromptReboot()));
  QObject::connect(dialog, SIGNAL(backTapped()), dialog, SLOT(deleteLater()));
  QObject::connect(dialog, SIGNAL(closeTapped()), this, SLOT(maybePromptReboot()));
  QObject::connect(dialog, SIGNAL(closeTapped()), this, SIGNAL(closeRequested()));

  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(0, chrome.top(), 0, chrome.bottom());
  layout->setSpacing(0);

  header = new Label(Label::Medium, "");
  header->setContentsMargins(14, 10, 14, 6);
  layout->addWidget(header);

  // Edit footer sits in the TOP region, directly under the header — NOT at the
  // bottom of the layout. Nickel's KeyboardFrame paints over the bottom band
  // without reflowing our content, so an editRow placed after `pages` would be
  // occluded by the on-screen keyboard (the donor's searchdialog.cc keeps its
  // editable field up top for the same reason). The single-line editor + the
  // text-only Delete affordance stay hidden until a row is tapped; the Nickel
  // keyboard is built once and reused per edit.
  editRow = new QWidget(this);
  QVBoxLayout *editLayout = new QVBoxLayout(editRow);
  editLayout->setContentsMargins(14, 6, 14, 6);
  editLayout->setSpacing(4);
  editPrompt = new Label(Label::Small, "");
  editLayout->addWidget(editPrompt);
  QHBoxLayout *editControls = new QHBoxLayout();
  editControls->setSpacing(12);
  editLineEdit = construct_TouchLineEdit(nullptr);
  editControls->addWidget(editLineEdit, 1);
  deleteButton = construct_N3ButtonLabel(editRow);
  deleteButton->setText("Delete line");
  editControls->addWidget(deleteButton, 0);
  editLayout->addLayout(editControls);
  layout->addWidget(editRow);
  editRow->hide();
  QObject::connect(deleteButton, SIGNAL(tapped(bool)), this, SLOT(deleteCurrentLine()));

  pages = new PagedStack(this);
  layout->addWidget(pages, 1);
  QObject::connect(pages, &PagedStack::requestPage, this, &ConfigDialog::requestPage);
  QObject::connect(pages, &PagedStack::afterLayout, this, &ConfigDialog::onFirstLayout);

  hint = new Label(Label::Small, "");
  hint->setContentsMargins(14, 6, 14, 6);
  hint->setProperty("style", "italic");
  layout->addWidget(hint);

  QHBoxLayout *buttons = new QHBoxLayout();
  buttons->setContentsMargins(14, 6, 14, 6);
  buttons->setSpacing(16);
  buttons->addStretch(1);
  // "Create from example" seeds a missing, template-bearing file (CONFIG.md §3.x).
  // Same footer-button pattern as Add line; hidden until a not-created file with a
  // template is shown.
  createButton = construct_N3ButtonLabel(this);
  createButton->setProperty("primaryButton", true);
  createButton->setText("Create from example");
  buttons->addWidget(createButton);
  createButton->hide();
  QObject::connect(createButton, SIGNAL(tapped(bool)), this, SLOT(createFromTemplate()));
  addLineButton = construct_N3ButtonLabel(this);
  addLineButton->setProperty("primaryButton", true);
  addLineButton->setText("Add line");
  buttons->addWidget(addLineButton);
  layout->addLayout(buttons);
  addLineButton->hide();
  QObject::connect(addLineButton, SIGNAL(tapped(bool)), this, SLOT(beginAppend()));

  // buildKeyboardFrame wires editLineEdit's tap → showKeyboard and the keyboard's
  // "Save"/go → hideKeyboard + commit() (Dialog base, same path as browse search).
  buildKeyboardFrame(editLineEdit, "Save");

  loadList();
}

void ConfigDialog::onFirstLayout() {
  if (laidOut) {
    return;
  }
  laidOut = true;
  QObject::disconnect(pages, &PagedStack::afterLayout, this, &ConfigDialog::onFirstLayout);
  if (dataReady) {
    render();
  }
}

void ConfigDialog::loadList() {
  KpmProcess *p = KpmProcess::configList(id); // read-only, offline (CONFIG.md §3.2)
  QObject::connect(p, &KpmProcess::response, this, &ConfigDialog::onConfigList);
  QObject::connect(p, &KpmProcess::failure, this, &ConfigDialog::onProcessFailure);
}

void ConfigDialog::onConfigList(int exitCode, QJsonObject payload) {
  (void)exitCode;
  configs = payload.value("configs").toArray();
  dataReady = true;

  if (configs.isEmpty()) {
    header->setText(name);
    pages->setStatusText("No editable configuration for this package.");
    return;
  }
  if (configs.size() == 1) {
    openFile(1); // single file: skip the picker and open it straight away (§4.2)
    return;
  }
  mode = FileList;
  header->setText("Configuration files");
  if (laidOut) {
    render();
  }
}

void ConfigDialog::openFile(int index) {
  if (index < 1 || index > configs.size()) {
    return;
  }
  QJsonObject file = configs.at(index - 1).toObject();
  currentSelector = file.value("name").toString();
  KpmProcess *p = KpmProcess::configShow(id, currentSelector);
  QObject::connect(p, &KpmProcess::response, this, &ConfigDialog::onConfigShow);
  QObject::connect(p, &KpmProcess::failure, this, &ConfigDialog::onProcessFailure);
}

void ConfigDialog::onConfigShow(int exitCode, QJsonObject payload) {
  (void)exitCode;
  showEntries(payload.value("file").toObject(), payload.value("entries").toArray());
}

void ConfigDialog::showEntries(const QJsonObject &fileObj, const QJsonArray &fileEntries) {
  mode = Entries;
  entries = fileEntries;
  currentFormat = fileObj.value("format").toString();
  currentReload = fileObj.value("reload").toString();
  currentExists = fileObj.value("exists").toBool();
  currentHasTemplate = fileObj.value("has_template").toBool();
  if (currentSelector.isEmpty()) {
    currentSelector = fileObj.value("name").toString();
  }
  header->setText(fileObj.value("name").toString());

  // Reload hint (§4.5): reboot files get the standard prompt on leaving; auto
  // files just note they apply on their own.
  if (currentReload == QStringLiteral("reboot")) {
    hint->setText("Changes need a reboot to take effect.");
  } else {
    hint->setText("Changes take effect automatically.");
  }

  // "Add line" is a text-file affordance only (§4.3); ini keys are edited in place.
  addLineButton->setVisible(currentFormat == QStringLiteral("text"));

  // "Create from example" is offered only when the file is missing but declares a
  // seed template (CONFIG.md §3.x). A missing file with no template keeps today's
  // behavior (the "not created" status / create-on-first-edit path).
  createButton->setVisible(!currentExists && currentHasTemplate);

  endEdit();
  if (laidOut) {
    render();
  }
}

void ConfigDialog::render() {
  endEdit();
  pages->clear();
  pages->next();
}

void ConfigDialog::requestPage(int index) {
  if (!dataReady) {
    return;
  }

  int count = mode == FileList ? configs.size() : entries.size();
  if (count == 0) {
    pages->setStatusText(mode == FileList ? "No configuration files." : "(empty file — nothing to edit yet)");
    return;
  }

  // Page size from a dummy row's height, like BrowseDialog::requestPage.
  QWidget *dummy = mode == FileList ? static_cast<QWidget *>(new ConfigFileRow(QJsonObject(), 0, this))
                                    : static_cast<QWidget *>(new ConfigEntryRow(QJsonObject(), currentFormat, this));
  int rowHeight = dummy->sizeHint().height();
  dummy->deleteLater();
  if (rowHeight <= 0) {
    rowHeight = 1;
  }
  int limit = pages->getAvailableHeight() / rowHeight;
  if (limit < 1) {
    limit = 1;
  }

  int total = (count + limit - 1) / limit;
  pages->setTotal(total);

  int start = (index - 1) * limit;
  if (start < 0 || start >= count) {
    return;
  }
  int end = qMin(start + limit, count);

  QWidget *box = new QWidget(pages);
  QVBoxLayout *v = new QVBoxLayout(box);
  v->setContentsMargins(0, 0, 0, 0);
  v->setSpacing(0);

  for (int i = start; i < end; i++) {
    if (mode == FileList) {
      ConfigFileRow *row = new ConfigFileRow(configs.at(i).toObject(), i + 1);
      QObject::connect(row, &ConfigFileRow::selected, this, &ConfigDialog::openFile);
      v->addWidget(row);
    } else {
      ConfigEntryRow *row = new ConfigEntryRow(entries.at(i).toObject(), currentFormat);
      QObject::connect(row, &ConfigEntryRow::selected, this, &ConfigDialog::beginEdit);
      v->addWidget(row);
    }
  }
  v->addStretch(1);
  pages->addPage(box);
}

// beginEdit swaps the footer to the single-line editor pre-filled with the
// entry's current (real, unmasked) value and raises the keyboard (§4.4).
void ConfigDialog::beginEdit(QJsonObject entry) {
  if (currentFormat == QStringLiteral("ini")) {
    editMode = EditIniKey;
    editSection = entry.value("section").toString();
    editKey = entry.value("key").toString();
    QString label = editSection.isEmpty() ? editKey : editSection + QStringLiteral(" · ") + editKey; // ·
    editPrompt->setText("Editing " + label);
    deleteButton->hide();
  } else {
    editMode = EditTextLine;
    editLine = entry.value("line").toInt();
    editPrompt->setText("Editing line " + QString::number(editLine));
    deleteButton->show();
  }
  editLineEdit->setText(entry.value("value").toString());
  editRow->show();
  showKeyboard();
  editLineEdit->setFocus();
}

void ConfigDialog::beginAppend() {
  editMode = EditTextAppend;
  editPrompt->setText("New line");
  deleteButton->hide();
  editLineEdit->setText("");
  editRow->show();
  showKeyboard();
  editLineEdit->setFocus();
}

// createFromTemplate seeds a missing file from its declared example via
// `kpm config init`. On success onSetDone re-reads the file, so the seeded lines
// render for editing and the button hides (the file now exists) — CONFIG.md §3.x.
void ConfigDialog::createFromTemplate() {
  KpmProcess *p = KpmProcess::configInit(id, currentSelector);
  if (!p) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setActionsEnabled(false);
  QObject::connect(p, &KpmProcess::response, this, &ConfigDialog::onSetDone);
  QObject::connect(p, &KpmProcess::failure, this, &ConfigDialog::onProcessFailure);
}

void ConfigDialog::commit() {
  if (editMode == EditNone) {
    return; // a stray keyboard commit with nothing being edited
  }
  QString value = editLineEdit->text();
  KpmProcess *p = nullptr;
  switch (editMode) {
  case EditIniKey:
    p = KpmProcess::configSetKey(id, currentSelector, editSection, editKey, value);
    break;
  case EditTextLine:
    p = KpmProcess::configSetLine(id, currentSelector, editLine, value);
    break;
  case EditTextAppend:
    p = KpmProcess::configAppendLine(id, currentSelector, value);
    break;
  case EditNone:
    return;
  }
  if (!p) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setActionsEnabled(false);
  QObject::connect(p, &KpmProcess::response, this, &ConfigDialog::onSetDone);
  QObject::connect(p, &KpmProcess::failure, this, &ConfigDialog::onProcessFailure);
}

void ConfigDialog::deleteCurrentLine() {
  if (editMode != EditTextLine) {
    return;
  }
  KpmProcess *p = KpmProcess::configDeleteLine(id, currentSelector, editLine);
  if (!p) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "kpm is busy — try again in a moment.");
    return;
  }
  setActionsEnabled(false);
  QObject::connect(p, &KpmProcess::response, this, &ConfigDialog::onSetDone);
  QObject::connect(p, &KpmProcess::failure, this, &ConfigDialog::onProcessFailure);
}

void ConfigDialog::onSetDone(int exitCode, QJsonObject payload) {
  setActionsEnabled(true);
  if (exitCode == 2 || !payload.value("failed").toArray().isEmpty()) {
    ConfirmationDialogFactory__showErrorDialog("kpm", "The edit could not be saved. See the kpm log for details.");
  } else if (payload.value("reboot_required").toBool()) {
    rebootPending = true; // prompt is deferred to leaving the dialog (§4.5)
  }
  // Re-read the file and repaint so the row shows the saved value (§4.4).
  KpmProcess *p = KpmProcess::configShow(id, currentSelector);
  QObject::connect(p, &KpmProcess::response, this, &ConfigDialog::onConfigShow);
  QObject::connect(p, &KpmProcess::failure, this, &ConfigDialog::onProcessFailure);
}

void ConfigDialog::onProcessFailure(QString reason) {
  (void)reason; // KpmProcess already showed the error dialog (§4)
  setActionsEnabled(true);
  if (!dataReady) {
    pages->setStatusText("Failed to load configuration.");
  }
}

void ConfigDialog::endEdit() {
  editMode = EditNone;
  if (editRow) {
    editRow->hide();
  }
}

void ConfigDialog::setActionsEnabled(bool enabled) {
  if (addLineButton) {
    addLineButton->setEnabled(enabled);
  }
  if (createButton) {
    createButton->setEnabled(enabled);
  }
  if (deleteButton) {
    deleteButton->setEnabled(enabled);
  }
}

void ConfigDialog::maybePromptReboot() {
  if (!rebootPending) {
    return;
  }
  rebootPending = false;
  ConfirmationDialog *d = ConfirmationDialogFactory__getConfirmationDialog(nullptr);
  ConfirmationDialog__setTitle(d, "kpm");
  ConfirmationDialog__setText(d, "Configuration changed. Reboot now to apply?");
  ConfirmationDialog__setAcceptButtonText(d, "Reboot now");
  ConfirmationDialog__setRejectButtonText(d, "Later");
  QObject::connect(d, &QDialog::accepted, [] { rebootDevice(); });
  QObject::connect(d, &QDialog::accepted, d, &QDialog::deleteLater);
  QObject::connect(d, &QDialog::rejected, d, &QDialog::deleteLater);
  d->open();
}
