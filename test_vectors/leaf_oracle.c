// This test helper includes the actual native implementation so tests can
// compare private leaf layouts without changing or copying the native code.
#include "../../wh.c"

int main(void) {
  // The production slab aligns leaves to cache lines; AVX slot repair uses
  // aligned stores to ss. A plain stack struct only guarantees 8-byte alignment.
  _Alignas(64) struct wormleaf leaf = {0};
  struct kv *records[256] = {0};
  unsigned tags[256] = {0};
  char op;
  unsigned id, tag;
  unsigned width = 3;
  while (scanf(" %c %u %u", &op, &id, &tag) == 3) {
    if (op == 'F') {
      if (tag != 3 && tag != 8) return 3;
      width = tag;
      continue;
    } else if (op == 'I') {
      char key[32];
      snprintf(key, sizeof(key), "key-%0*u", (int)width, id);
      records[id] = calloc(1, sizeof(struct kv) + sizeof(key));
      kv_refill(records[id], key, strlen(key), NULL, 0);
      tags[id] = tag;
      const u32 n = leaf.nr_keys;
      wormleaf_insert_e13(&leaf, entry13(tag, ptr_to_u64(records[id])));
      if (n == leaf.nr_sorted && (!n || kv_compare(records[id], wormleaf_kv_at_is(&leaf, n-1)) > 0))
        leaf.nr_sorted++;
    } else if (op == 'D') {
      u32 slot = wormleaf_search_ih(&leaf, entry13(tags[id], ptr_to_u64(records[id])));
      free(wormleaf_remove_ih(&leaf, slot));
      records[id] = NULL;
    } else if (op == 'S') {
      wormleaf_sync_sorted(&leaf);
    }
    printf("%u %u", leaf.nr_keys, leaf.nr_sorted);
    for (unsigned i = 0; i < WH_KPN; i++) {
      if (leaf.hs[i].e1) {
        struct kv *kv = wormleaf_kv_at_ih(&leaf, i);
        printf(" %u:%u:%.*s", i, (unsigned)leaf.hs[i].e1, kv->klen, kv->kv);
      }
    }
    printf(" |");
    for (unsigned i = 0; i < leaf.nr_keys; i++) printf(" %u", leaf.ss[i]);
    puts("");
  }
  for (unsigned i = 0; i < 256; i++) free(records[i]);
  return 0;
}
