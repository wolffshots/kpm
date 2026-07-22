# hook/sim — desktop-simulator build (Tier-3 off-device testing).
#
# Compiles the REAL browse/detail/kpmprocess/widget sources UNMODIFIED against
# desktop Qt5 (Ubuntu qtbase5-dev), linked with the sim Nickel shim (nickelstub)
# and entry point (main). Device build (hook/Makefile) is untouched.
#
# NOT -Werror: Ubuntu's Qt 5.15 emits deprecation warnings the device Qt 5.2.1
# doesn't (e.g. QProcess::error); -Wno-deprecated-declarations silences them for
# the SIM build only. moc mirrors the device convention (moc each Q_OBJECT header
# into a separate translation unit); moc embeds the header path relative to the
# output dir, so build/ output resolves the real ../src headers automatically.

BUILD    := build
CXX      := g++
MOC      := moc
QT_CFLAGS := $(shell pkg-config --cflags Qt5Widgets)
QT_LIBS   := $(shell pkg-config --libs Qt5Widgets)

# -MMD -MP emit per-object header-dependency files (build/*.d), pulled in below so
# a device-header change (e.g. widgets/dialog.h) recompiles every dependent .cc.
# Without this, incremental builds keep stale objects with mismatched class layouts.
CXXFLAGS := -std=gnu++11 -fPIC -DNKPM_SIM $(QT_CFLAGS) \
            -Wall -Wextra -Wno-deprecated-declarations -Wno-missing-field-initializers \
            -MMD -MP -I. -I../src

vpath %.cc ../src ../src/widgets .
vpath %.h  ../src ../src/widgets .

# Device sources compiled unmodified + sim shim/entry.
CC_SRCS  := browsedialog.cc detaildialog.cc configdialog.cc kpmprocess.cc \
            dialog.cc label.cc elidedlabel.cc packagerow.cc configrow.cc pagedstack.cc \
            nickelstub.cc main.cc
OBJS     := $(addprefix $(BUILD)/,$(CC_SRCS:.cc=.o))

# Headers carrying Q_OBJECT (moc'd), one translation unit each.
MOC_HDRS := browsedialog.h detaildialog.h configdialog.h kpmprocess.h \
            dialog.h label.h elidedlabel.h packagerow.h configrow.h pagedstack.h \
            nickelstub.h
MOC_OBJS := $(addprefix $(BUILD)/moc_,$(MOC_HDRS:.h=.o))

TARGET := nkpm-sim

.PHONY: all clean
all: $(TARGET)

$(TARGET): $(OBJS) $(MOC_OBJS)
	$(CXX) -o $@ $^ $(QT_LIBS)

$(BUILD):
	mkdir -p $(BUILD)

$(BUILD)/%.o: %.cc | $(BUILD)
	$(CXX) $(CXXFLAGS) -c $< -o $@

$(BUILD)/moc_%.cpp: %.h | $(BUILD)
	$(MOC) $< -o $@

$(BUILD)/moc_%.o: $(BUILD)/moc_%.cpp
	$(CXX) $(CXXFLAGS) -c $< -o $@

.SECONDARY:

-include $(OBJS:.o=.d) $(MOC_OBJS:.o=.d)

clean:
	rm -rf $(BUILD) $(TARGET)
