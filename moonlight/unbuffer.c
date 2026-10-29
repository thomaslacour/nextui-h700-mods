/* unbuffer.c — libunbuffer.so, chargée par LD_PRELOAD pour « moonlight pair ».
 *
 * moonlight écrit le PIN avec printf puis attend la saisie sur le PC ; vers un
 * fichier, stdout est en tampon complet et le PIN n'arriverait qu'à la fin.
 * Le pak lit ce fichier pendant l'attente : stdout passe en non tamponné.
 */
#include <stdio.h>

__attribute__((constructor)) static void unbuffer(void) {
    setvbuf(stdout, NULL, _IONBF, 0);
}
