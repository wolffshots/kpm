# NICKEL-UI.md §1/§8 — modeled on NickelHardcover's hook/Makefile (MIT).
# Builds libnkpm.so, the NickelKPM Qt hook. Warning-clean is a gate (§8): the
# NickelHook toolchain is invoked with -Wall -Wextra -Werror.
#
# Build (NICKEL-UI.md §8):
#   git submodule update --init --recursive
#   podman run --rm -v "$PWD":"$PWD" -w "$PWD/hook" ghcr.io/pgaskin/nickeltc:1.0 make
#
# Qt 6 build for firmware 6.x (QT6-FIRMWARE6.md section 4), libnkpm-qt6.so.
# Run "make clean" first: the two builds share the object files.
#   podman run --rm -v "$PWD":"$PWD" -w "$PWD/hook" ghcr.io/nicoverbruggen/kobuild:qt6-v0.5.3@sha256:b939301323e1bed0bf6b0bee43b8791db2efdf01231d4245c1f972d57433644a make NH_QT_MAJOR=6

include ./NickelHook/NickelHook.mk

override LIBRARY  := $(if $(filter 6,$(NH_QT_MAJOR)),libnkpm-qt6.so,libnkpm.so)
override SOURCES  += $(wildcard src/*.cc src/**/*.cc)
override CFLAGS   += -Wall -Wextra -Werror -Wno-missing-field-initializers -fvisibility=hidden
override CXXFLAGS += -Wall -Wextra -Werror -Wno-missing-field-initializers -fvisibility=hidden -fvisibility-inlines-hidden
override PKGCONF  += Qt$(NH_QT_MAJOR)Widgets
override MOCS     += $(wildcard src/*.h src/**/*.h)
override QRCS     += res/nkpm.qrc

include ./NickelHook/NickelHook.mk
