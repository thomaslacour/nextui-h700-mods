/*
 * pad-probe — reads an external pad through the console's own SDL2, so the
 * indices it prints (bN, aN, hN.M) and the GUID are exactly what NextUI,
 * minarch and the ports see. Used by the Pad Setup pak.
 *
 *   pad-probe list [db]
 *       one line per external pad: index<TAB>guid<TAB>name<TAB>known
 *       (known = 1 if [db] or SDL already has a mapping for it)
 *   pad-probe wait <guid> <seconds> <kind> [used...]
 *       waits for one new input on that pad and prints "<binding> <raw>":
 *         binding = what goes after "name:" in the SDL mapping line
 *         raw     = b3 | h0.4 | a2+ | a2-  (pass it back in [used...] so the
 *                   same input is not taken twice)
 *       kind: button | trigger | dpad | stick
 *       or prints "skip" (timeout or console A), "cancel" (console B),
 *       "gone" (pad not found).
 *   pad-probe test <guid> <seconds> <db>
 *       loads <db>, opens the pad as a GameController and prints the SDL
 *       name of each control used (a, b, lefttrigger, leftx-...), one per
 *       line, until <seconds> pass or the console's B is pressed. Checks a
 *       saved line end to end.
 *
 * The console's own keys are read from evdev (ANBERNIC-keys, A = 304,
 * B = 305) only to skip or cancel; the pad itself is read through SDL.
 */
#include <SDL2/SDL.h>
#include <dirent.h>
#include <fcntl.h>
#include <linux/input.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define REST 8000     /* axis counts as back at rest within this distance */
#define MOVED 16000   /* axis counts as moved beyond this distance */
#define MAXIN 64

static int is_console(const char *name) { return name && strcmp(name, "ANBERNIC-keys") == 0; }

/* evdev node of the console keys, or -1 */
static int console_keys(void)
{
    DIR *d = opendir("/sys/class/input");
    struct dirent *e;
    int fd = -1;
    if (!d) return -1;
    while (fd < 0 && (e = readdir(d))) {
        char path[300], name[128] = "";
        FILE *f;
        if (strncmp(e->d_name, "event", 5)) continue;
        snprintf(path, sizeof path, "/sys/class/input/%s/device/name", e->d_name);
        if (!(f = fopen(path, "r"))) continue;
        if (fgets(name, sizeof name, f)) name[strcspn(name, "\n")] = 0;
        fclose(f);
        if (!is_console(name)) continue;
        snprintf(path, sizeof path, "/dev/input/%s", e->d_name);
        fd = open(path, O_RDONLY | O_NONBLOCK);
    }
    closedir(d);
    return fd;
}

/* 1 = A released, 2 = B released, 0 = nothing */
static int console_key(int fd)
{
    struct input_event ev;
    int r = 0;
    if (fd < 0) return 0;
    while (read(fd, &ev, sizeof ev) == sizeof ev)
        if (ev.type == EV_KEY && ev.value == 0) {
            if (ev.code == BTN_SOUTH) r = r ? r : 1;
            if (ev.code == BTN_EAST) r = 2;
        }
    return r;
}

static int cmd_list(const char *db)
{
    int i, n;
    if (db) SDL_GameControllerAddMappingsFromFile(db);
    n = SDL_NumJoysticks();
    for (i = 0; i < n; i++) {
        char guid[64];
        const char *name = SDL_JoystickNameForIndex(i);
        if (is_console(name)) continue;
        SDL_JoystickGetGUIDString(SDL_JoystickGetDeviceGUID(i), guid, sizeof guid);
        printf("%d\t%s\t%s\t%d\n", i, guid, name ? name : "Unknown pad", SDL_IsGameController(i) ? 1 : 0);
    }
    return 0;
}

static int used(char **list, int n, const char *raw)
{
    int i;
    for (i = 0; i < n; i++)
        if (strcmp(list[i], raw) == 0) return 1;
    return 0;
}

static int cmd_wait(const char *want, int seconds, const char *kind, char **usedv, int usedn)
{
    SDL_Joystick *j = NULL;
    int i, n = SDL_NumJoysticks(), nb, na, nh, keys;
    Sint16 base[MAXIN];
    int armed_b[MAXIN], armed_a[MAXIN], armed_h[MAXIN];
    Uint32 end;

    for (i = 0; i < n && !j; i++) {
        char guid[64];
        if (is_console(SDL_JoystickNameForIndex(i))) continue;
        SDL_JoystickGetGUIDString(SDL_JoystickGetDeviceGUID(i), guid, sizeof guid);
        if (strcmp(guid, want) == 0) j = SDL_JoystickOpen(i);
    }
    if (!j) { puts("gone"); return 1; }
    nb = SDL_min(SDL_JoystickNumButtons(j), MAXIN);
    na = SDL_min(SDL_JoystickNumAxes(j), MAXIN);
    nh = SDL_min(SDL_JoystickNumHats(j), MAXIN);
    keys = console_keys();

    /* Let the initial state settle; anything already held must be released
       before it can count (armed). */
    SDL_Delay(120);
    SDL_JoystickUpdate();
    for (i = 0; i < na; i++) {
        Sint16 v = SDL_JoystickGetAxis(j, i);
        if (!SDL_JoystickGetAxisInitialState(j, i, &base[i])) base[i] = v;
        armed_a[i] = abs(v - base[i]) < REST;
    }
    for (i = 0; i < nb; i++) armed_b[i] = !SDL_JoystickGetButton(j, i);
    for (i = 0; i < nh; i++) armed_h[i] = SDL_JoystickGetHat(j, i) == SDL_HAT_CENTERED;
    console_key(keys); /* drop console presses made before the prompt */

    end = SDL_GetTicks() + (Uint32)seconds * 1000;
    while (SDL_GetTicks() < end) {
        char raw[16], bind[20];
        int c = console_key(keys);
        if (c == 1) { puts("skip"); return 0; }
        if (c == 2) { puts("cancel"); return 0; }
        if (!SDL_JoystickGetAttached(j)) { puts("gone"); return 1; }
        SDL_JoystickUpdate();
        raw[0] = 0;

        for (i = 0; i < nb && !raw[0]; i++) {
            int down = SDL_JoystickGetButton(j, i);
            if (!down) { armed_b[i] = 1; continue; }
            if (!armed_b[i]) continue;
            armed_b[i] = 0;
            snprintf(raw, sizeof raw, "b%d", i);
            if (used(usedv, usedn, raw) || !strcmp(kind, "stick")) { raw[0] = 0; continue; }
            snprintf(bind, sizeof bind, "b%d", i);
        }
        for (i = 0; i < nh && !raw[0]; i++) {
            Uint8 h = SDL_JoystickGetHat(j, i);
            if (h == SDL_HAT_CENTERED) { armed_h[i] = 1; continue; }
            if (!armed_h[i] || (h & (h - 1))) continue; /* ignore diagonals */
            armed_h[i] = 0;
            snprintf(raw, sizeof raw, "h%d.%d", i, h);
            if (used(usedv, usedn, raw) || !strcmp(kind, "stick")) { raw[0] = 0; continue; }
            snprintf(bind, sizeof bind, "h%d.%d", i, h);
        }
        /* Axes: take the one moved the most, so a slightly diagonal push
           still gives the intended axis. */
        {
            int best = -1, bestd = MOVED;
            for (i = 0; i < na; i++) {
                int d = SDL_JoystickGetAxis(j, i) - base[i];
                char r[16];
                if (abs(d) < REST) { armed_a[i] = 1; continue; }
                if (!armed_a[i] || abs(d) <= bestd) continue;
                snprintf(r, sizeof r, "a%d%c", i, d > 0 ? '+' : '-');
                if (used(usedv, usedn, r)) continue;
                best = i; bestd = abs(d);
            }
            if (!raw[0] && best >= 0) {
                int d = SDL_JoystickGetAxis(j, best) - base[best];
                char sign = d > 0 ? '+' : '-';
                armed_a[best] = 0;
                snprintf(raw, sizeof raw, "a%d%c", best, sign);
                if (!strcmp(kind, "stick"))
                    /* asked direction is right/down = positive */
                    snprintf(bind, sizeof bind, "a%d%s", best, d > 0 ? "" : "~");
                else if (!strcmp(kind, "trigger") && base[best] < -32768 + REST)
                    /* full-range trigger, at rest at the minimum */
                    snprintf(bind, sizeof bind, "a%d", best);
                else
                    snprintf(bind, sizeof bind, "%ca%d", sign, best);
            }
        }
        if (raw[0]) {
            printf("%s %s\n", bind, raw);
            return 0;
        }
        SDL_Delay(10);
    }
    puts("skip");
    return 0;
}

static int cmd_test(const char *want, int seconds, const char *db)
{
    SDL_GameController *gc = NULL;
    int i, n, keys;
    Uint32 end;
    SDL_Event e;

    printf("mappings=%d\n", SDL_GameControllerAddMappingsFromFile(db));
    n = SDL_NumJoysticks();
    for (i = 0; i < n && !gc; i++) {
        char guid[64];
        if (is_console(SDL_JoystickNameForIndex(i))) continue;
        SDL_JoystickGetGUIDString(SDL_JoystickGetDeviceGUID(i), guid, sizeof guid);
        if (strcmp(guid, want)) continue;
        if (!SDL_IsGameController(i)) { puts("unmapped"); return 1; }
        gc = SDL_GameControllerOpen(i);
    }
    if (!gc) { puts("gone"); return 1; }
    {
        char *m = SDL_GameControllerMapping(gc);
        printf("mapping=%s\n", m ? m : "?");
        SDL_free(m);
    }
    fflush(stdout);
    keys = console_keys();
    end = SDL_GetTicks() + (Uint32)seconds * 1000;
    while (SDL_GetTicks() < end && console_key(keys) != 2) {
        while (SDL_PollEvent(&e)) {
            if (e.type == SDL_CONTROLLERBUTTONDOWN)
                printf("%s\n", SDL_GameControllerGetStringForButton(e.cbutton.button));
            else if (e.type == SDL_CONTROLLERAXISMOTION) {
                /* print each push once, when it crosses the threshold */
                static Sint16 last[SDL_CONTROLLER_AXIS_MAX];
                Sint16 v = e.caxis.value, *l = &last[e.caxis.axis];
                if (abs(v) > MOVED && (abs(*l) <= MOVED || (v > 0) != (*l > 0)))
                    printf("%s%c\n", SDL_GameControllerGetStringForAxis(e.caxis.axis), v > 0 ? '+' : '-');
                *l = v;
            }
            fflush(stdout);
        }
        SDL_Delay(10);
    }
    SDL_GameControllerClose(gc);
    return 0;
}

int main(int argc, char **argv)
{
    int rc;
    if (argc < 2) {
        fprintf(stderr, "usage: pad-probe list [db] | wait <guid> <seconds> <kind> [used...] | test <guid> <seconds> <db>\n");
        return 2;
    }
    SDL_SetHint(SDL_HINT_JOYSTICK_ALLOW_BACKGROUND_EVENTS, "1");
    if (SDL_Init(SDL_INIT_JOYSTICK | SDL_INIT_GAMECONTROLLER) < 0) {
        fprintf(stderr, "pad-probe: %s\n", SDL_GetError());
        return 1;
    }
    if (!strcmp(argv[1], "list"))
        rc = cmd_list(argc > 2 ? argv[2] : NULL);
    else if (!strcmp(argv[1], "test") && argc >= 5)
        rc = cmd_test(argv[2], atoi(argv[3]), argv[4]);
    else if (!strcmp(argv[1], "wait") && argc >= 5)
        rc = cmd_wait(argv[2], atoi(argv[3]), argv[4], argv + 5, argc - 5);
    else {
        fprintf(stderr, "pad-probe: unknown command\n");
        rc = 2;
    }
    SDL_Quit();
    return rc;
}
