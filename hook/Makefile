# NICKEL-UI.md §1/§8 — modeled on NickelHardcover's hook/Makefile (MIT).
# Builds libnkpm.so, the NickelKPM Qt hook. Warning-clean is a gate (§8): the
# NickelHook toolchain is invoked with -Wall -Wextra -Werror.
#
# Build (NICKEL-UI.md §8):
#   git submodule update --init --recursive
#   podman run --rm -v "$PWD":"$PWD" -w "$PWD/hook" ghcr.io/pgaskin/nickeltc:1.0 make

include ./NickelHook/NickelHook.mk

override LIBRARY  := libnkpm.so
override SOURCES  += $(wildcard src/*.cc src/**/*.cc)
override CFLAGS   += -Wall -Wextra -Werror -Wno-missing-field-initializers -fvisibility=hidden
override CXXFLAGS += -Wall -Wextra -Werror -Wno-missing-field-initializers -fvisibility=hidden -fvisibility-inlines-hidden
override PKGCONF  += Qt5Widgets
override MOCS     += $(wildcard src/*.h src/**/*.h)
override QRCS     += res/nkpm.qrc

include ./NickelHook/NickelHook.mk
