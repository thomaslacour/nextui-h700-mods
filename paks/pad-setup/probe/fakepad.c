/*
 * fakepad — test helper (not installed): a virtual Bluetooth pad made with
 * uinput, shaped like the OhSnap MCON (Xbox Series ids 045e:0b13,
 * KEY_RECORD declared before the gamepad buttons, analog triggers at rest at
 * their minimum) but with version 0999, so it gets its own GUID. Lets the
 * Pad Setup flow be tested over adb without touching a real pad.
 *
 *   fakepad <name> <hold-seconds> [step...]
 *     w<ms>         pause
 *     k<code>       press then release a key (e.g. k304 = BTN_SOUTH)
 *     a<code>=<v>   move an axis to v, then back to rest (0)
 *     h<code>=<v>   hat (16 = HAT0X, 17 = HAT0Y) to v (-1/1), then back to 0
 *   The device stays for <hold-seconds> after the steps.
 */
#include <fcntl.h>
#include <linux/uinput.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static int fd;

static void emit(int type, int code, int val)
{
    struct input_event ev;
    memset(&ev, 0, sizeof ev);
    ev.type = type;
    ev.code = code;
    ev.value = val;
    if (write(fd, &ev, sizeof ev) < 0) perror("write");
    memset(&ev, 0, sizeof ev);
    ev.type = EV_SYN;
    if (write(fd, &ev, sizeof ev) < 0) perror("write");
}

static void absdef(struct uinput_user_dev *d, int code, int min, int max)
{
    ioctl(fd, UI_SET_ABSBIT, code);
    d->absmin[code] = min;
    d->absmax[code] = max;
}

int main(int argc, char **argv)
{
    static const int keys[] = {KEY_RECORD, BTN_SOUTH, BTN_EAST, BTN_NORTH, BTN_WEST,
                               BTN_TL, BTN_TR, BTN_SELECT, BTN_START, BTN_MODE,
                               BTN_THUMBL, BTN_THUMBR};
    struct uinput_user_dev d;
    unsigned i;
    int a;

    if (argc < 3) { fprintf(stderr, "usage: fakepad <name> <hold-seconds> [step...]\n"); return 2; }
    fd = open("/dev/uinput", O_WRONLY | O_NONBLOCK);
    if (fd < 0) { perror("/dev/uinput"); return 1; }
    memset(&d, 0, sizeof d);
    snprintf(d.name, sizeof d.name, "%s", argv[1]);
    d.id.bustype = BUS_BLUETOOTH;
    d.id.vendor = 0x045e;
    d.id.product = 0x0b13;
    d.id.version = 0x0999; /* not the real pad's 011b: its own GUID */
    ioctl(fd, UI_SET_EVBIT, EV_KEY);
    ioctl(fd, UI_SET_EVBIT, EV_ABS);
    for (i = 0; i < sizeof keys / sizeof *keys; i++) ioctl(fd, UI_SET_KEYBIT, keys[i]);
    absdef(&d, ABS_X, -32768, 32767);
    absdef(&d, ABS_Y, -32768, 32767);
    absdef(&d, ABS_Z, 0, 1023);
    absdef(&d, ABS_RX, -32768, 32767);
    absdef(&d, ABS_RY, -32768, 32767);
    absdef(&d, ABS_RZ, 0, 1023);
    absdef(&d, ABS_HAT0X, -1, 1);
    absdef(&d, ABS_HAT0Y, -1, 1);
    if (write(fd, &d, sizeof d) != sizeof d || ioctl(fd, UI_DEV_CREATE) < 0) { perror("create"); return 1; }
    sleep(1);

    for (a = 3; a < argc; a++) {
        const char *s = argv[a];
        int code, val;
        switch (s[0]) {
        case 'w':
            usleep(atoi(s + 1) * 1000);
            break;
        case 'k':
            code = atoi(s + 1);
            emit(EV_KEY, code, 1);
            usleep(150000);
            emit(EV_KEY, code, 0);
            break;
        case 'a':
        case 'h':
            if (sscanf(s + 1, "%d=%d", &code, &val) != 2) break;
            emit(EV_ABS, code, val);
            usleep(250000);
            emit(EV_ABS, code, 0);
            break;
        }
        fprintf(stderr, "fakepad: %s\n", s);
    }
    sleep(atoi(argv[2]));
    ioctl(fd, UI_DEV_DESTROY);
    close(fd);
    return 0;
}
