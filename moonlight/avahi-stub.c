/* avahi-stub.c — remplace libavahi-client.so.3 / libavahi-common.so.3 (absentes
 * de BaseOS) pour libgamestream de moonlight-embedded.
 *
 * Avahi ne sert qu'à la découverte mDNS des PC (« moonlight discover » ou
 * « stream » sans adresse). Le pak donne toujours l'adresse de l'hôte, et
 * Tailscale ne relaie pas le mDNS : chaque fonction échoue proprement, ce que
 * libgamestream traite comme « aucun hôte trouvé ».
 *
 * Seuls les symboles importés par libgamestream.so.4 sont définis (liste
 * relevée avec llvm-readelf --dyn-syms). La même bibliothèque est installée
 * sous les deux noms ; l'éditeur de liens dynamique résout les symboles dans
 * l'espace global, peu importe lequel des deux les fournit.
 */
#include <stddef.h>

#define AVAHI_ERR_NOT_SUPPORTED (-23)

void *avahi_simple_poll_new(void) { return NULL; }
void avahi_simple_poll_free(void *p) { (void)p; }
const void *avahi_simple_poll_get(void *p) { (void)p; return NULL; }
int avahi_simple_poll_loop(void *p) { (void)p; return -1; }
void avahi_simple_poll_quit(void *p) { (void)p; }

void *avahi_client_new(const void *poll, int flags, void *cb, void *userdata, int *error) {
    (void)poll; (void)flags; (void)cb; (void)userdata;
    if (error) *error = AVAHI_ERR_NOT_SUPPORTED;
    return NULL;
}
void avahi_client_free(void *c) { (void)c; }

void *avahi_service_browser_new(void *c, int iface, int proto, const char *type,
                                const char *domain, int flags, void *cb, void *userdata) {
    (void)c; (void)iface; (void)proto; (void)type; (void)domain; (void)flags; (void)cb; (void)userdata;
    return NULL;
}
void *avahi_service_browser_get_client(void *b) { (void)b; return NULL; }
int avahi_service_browser_free(void *b) { (void)b; return 0; }

void *avahi_service_resolver_new(void *c, int iface, int proto, const char *name,
                                 const char *type, const char *domain, int aproto,
                                 int flags, void *cb, void *userdata) {
    (void)c; (void)iface; (void)proto; (void)name; (void)type; (void)domain;
    (void)aproto; (void)flags; (void)cb; (void)userdata;
    return NULL;
}
int avahi_service_resolver_free(void *r) { (void)r; return 0; }

char *avahi_address_snprint(char *s, size_t length, const void *a) {
    (void)a;
    if (s && length) s[0] = '\0';
    return s;
}
