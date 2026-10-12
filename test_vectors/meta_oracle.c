// Directly exercise private native metadata updates using production allocators.
#include "../../wh.c"

static void hex(const u8 *key, u32 len) {
  if (!len) putchar('-');
  for (u32 i = 0; i < len; i++) printf("%02x", key[i]);
}
static void anchor(struct wormleaf *leaf) {
  if (!leaf) { printf("nil"); return; }
  hex(leaf->anchor->kv, leaf->anchor->klen);
}
static int cmp_meta(const void *a, const void *b) {
  const struct wormmeta *ma = *(const struct wormmeta **)a;
  const struct wormmeta *mb = *(const struct wormmeta **)b;
  u32 na = wormmeta_klen_load(ma), nb = wormmeta_klen_load(mb);
  int cmp = memcmp(wormmeta_keyref_load(ma)->kv, wormmeta_keyref_load(mb)->kv, na < nb ? na : nb);
  return cmp ? cmp : (na > nb) - (na < nb);
}
static void dump(struct wormhmap *hmap) {
  size_t count = 0;
  struct wormmeta **nodes = calloc(((size_t)hmap->mask+1)*8, sizeof(*nodes));
  for (u32 i = 0; i <= hmap->mask; i++)
    for (u32 j = 0; j < 8 && hmap->pmap[i].e[j]; j++) nodes[count++] = hmap->pmap[i].e[j];
  qsort(nodes, count, sizeof(*nodes), cmp_meta);
  printf("T %zu %u\n", count, hmap->maxplen);
  for (size_t i = 0; i < count; i++) {
    struct wormmeta *m = nodes[i];
    printf("M "); hex(wormmeta_keyref_load(m)->kv, wormmeta_klen_load(m));
    putchar(' '); anchor(wormmeta_lmost_load(m));
    putchar(' '); anchor(wormmeta_rmost_load(m));
    putchar(' '); anchor(wormmeta_lpath_load(m));
    printf(" %u %u", wormmeta_bitmin_load(m), wormmeta_bitmax_load(m));
    for (u32 token = 0; token < 256; token++) if (wormmeta_bm_test(m, token)) printf(" %u", token);
    putchar('\n');
  }
  free(nodes);
}
int main(void) {
  struct wormhole *map = whunsafe_create(NULL);
  if (!map) return 2;
  char op, encoded[513];
  u8 key[256];
  while (scanf(" %c %512s", &op, encoded) == 2) {
    u32 len = !strcmp(encoded, "-") ? 0 : strlen(encoded)/2;
    for (u32 i = 0; i < len; i++) { unsigned x; sscanf(encoded+2*i, "%2x", &x); key[i] = x; }
    if (op == 'I') {
      struct kv *a = wormhole_alloc_akey(len);
      kv_refill(a, key, len, NULL, 0);
      struct wormleaf *prev = map->leaf0;
      while (prev->next && kv_compare(prev->next->anchor, a) < 0) prev = prev->next;
      struct wormleaf *leaf = wormleaf_alloc(map, prev, prev->next, a);
      if (prev->next) prev->next->prev = leaf;
      prev->next = leaf;
      struct kv *mkey = wormhole_alloc_mkey(len);
      kv_dup2_key(a, mkey);
      wormmeta_split(map->hmap, leaf, mkey);
      if (!mkey->refcnt) wormhole_free_mkey(mkey);
      dump(map->hmap);
    } else if (op == 'D') {
      struct wormleaf *leaf = map->leaf0->next;
      while (leaf && (leaf->anchor->klen != len || memcmp(leaf->anchor->kv, key, len))) leaf = leaf->next;
      if (!leaf) return 3;
      leaf->prev->next = leaf->next;
      if (leaf->next) leaf->next->prev = leaf->prev;
      wormmeta_merge(map->hmap, leaf);
      wormleaf_free(map->slab_leaf, leaf);
      dump(map->hmap);
    } else if (op == 'J') {
      struct kref ref = {.ptr = key};
      struct wormmeta *m = wormhole_meta_lcp(map->hmap, &ref, len);
      printf("J "); anchor(wormhole_meta_down(map->hmap, &ref, m, len));
      printf(" %u %08x\n", ref.len, ref.hash32);
    }
  }
  wormhole_destroy(map);
  return 0;
}
