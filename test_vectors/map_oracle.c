// Exercise native safe APIs and dump private leaf layouts for differential tests.
#include "../../wh.c"

static u32 decode(const char *text, u8 *out) {
  if (!strcmp(text, "-")) return 0;
  u32 len = strlen(text)/2;
  for (u32 i = 0; i < len; i++) { unsigned x; sscanf(text+2*i, "%2x", &x); out[i] = x; }
  return len;
}
static void hex(const u8 *key, u32 len) {
  if (!len) putchar('-');
  for (u32 i = 0; i < len; i++) printf("%02x", key[i]);
}
static void layout(struct wormhole *map) {
  for (struct wormleaf *l = map->leaf0; l; l = l->next) {
    printf("A "); hex(l->anchor->kv, l->anchor->klen);
    printf(" %u %u\n", l->nr_keys, l->nr_sorted);
    for (u32 i = 0; i < WH_KPN; i++) if (l->hs[i].e1) {
      struct kv *kv = wormleaf_kv_at_ih(l, i);
      printf("H %u %u ", i, (unsigned)l->hs[i].e1); hex(kv->kv, kv->klen); putchar('\n');
    }
    printf("O"); for (u32 i = 0; i < l->nr_keys; i++) printf(" %u", l->ss[i]); putchar('\n');
  }
  puts("E");
}
struct merge_args { const u8 *key, *value; u32 klen, vlen, calls; bool store; struct kv *allocated; };
static struct kv *merge_value(struct kv *old, void *priv) {
  (void)old;
  struct merge_args *args = priv;
  args->calls++;
  if (!args->store) return NULL;
  args->allocated = kv_create(args->key, args->klen, args->value, args->vlen);
  return args->allocated;
}
static void inspect_value(struct kv *kv, void *priv) {
  u32 *calls = priv; (*calls)++;
  printf("I %u ", kv != NULL);
  if (kv) hex(kv_vptr(kv), kv->vlen); else putchar('-');
  printf(" %u\n", *calls);
}
static void update_value(struct kv *kv, void *priv) {
  u32 *calls = priv; (*calls)++;
  if (kv) { u8 *value = kv_vptr(kv); for (u32 i = 0; i < kv->vlen; i++) value[i] ^= 1; }
}
int main(void) {
  struct wormhole *map = wormhole_create(NULL);
  if (!map) return 2;
  struct wormref *ref = whsafe_ref(map), *iref = whsafe_ref(map);
  struct wormhole_iter *it = wormhole_iter_create(iref);
  char op, a[1025], b[1025];
  u8 ka[512], kb[512];
  while (scanf(" %c %1024s %1024s", &op, a, b) == 3) {
    u32 na = decode(a, ka), nb = !strcmp(b, "*") ? 0 : decode(b, kb);
    struct kref key, end;
    kref_ref_hash32(&key, ka, na); kref_ref_hash32(&end, kb, nb);
    if (op == 'P') {
      struct kv *kv = kv_create(ka, na, kb, nb);
      printf("P %u\n", whsafe_put(ref, kv)); free(kv);
    } else if (op == 'M' || op == 'N') {
      struct merge_args args = {.key = ka, .value = kb, .klen = na, .vlen = nb, .store = op == 'M'};
      bool result = whsafe_merge(ref, &key, merge_value, &args);
      printf("%c %u %u\n", op, result, args.calls); free(args.allocated);
    } else if (op == 'I') {
      u32 calls = 0; (void)whsafe_inpr(ref, &key, inspect_value, &calls);
    } else if (op == 'T') {
      u32 calls = 0; whsafe_iter_seek(it, &key);
      for (u32 i = 0; i < 2; i++) {
        bool result = wormhole_iter_inp(it, inspect_value, &calls);
        printf("T %u\n", result);
      }
      whsafe_iter_park(it);
    } else if (op == 'U') {
      u32 calls = 0; bool result = whsafe_inpw(ref, &key, update_value, &calls);
      printf("U %u %u\n", result, calls);
    } else if (op == 'D') {
      printf("D %u\n", whsafe_del(ref, &key));
    } else if (op == 'G') {
      struct kv *kv = whsafe_get(ref, &key, NULL);
      printf("G %u ", kv != NULL);
      if (kv) hex(kv_vptr(kv), kv->vlen); else putchar('-');
      putchar('\n'); free(kv);
    } else if (op == 'B') {
      printf("B %u\n", whsafe_probe(ref, &key));
    } else if (op == 'R') {
      printf("R %lu\n", whsafe_delr(ref, &key, !strcmp(b, "*") ? NULL : &end));
    } else if (op == 'S') {
      puts("S"); whsafe_iter_seek(it, &key);
      struct kv *kv;
      while ((kv = wormhole_iter_next(it, NULL))) {
        printf("K "); hex(kv->kv, kv->klen); putchar(' '); hex(kv_vptr(kv), kv->vlen); putchar('\n'); free(kv);
      }
      whsafe_iter_park(it); puts("E");
    } else if (op == 'A') {
      layout(map);
    }
  }
  whsafe_iter_destroy(it); wormhole_unref(iref); wormhole_unref(ref);
  wormhole_destroy(map);
  return 0;
}
