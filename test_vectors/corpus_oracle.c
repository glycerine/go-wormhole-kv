// Workload oracle: exercise the actual libc random() and lib.c random_u64().
#include <stdio.h>
#include <stdlib.h>
#include "../../lib.h"

int main(void) {
  unsigned nwords, nkeys;
  if (scanf("%u %u", &nwords, &nkeys) != 2 || !nwords) return 1;
  char (*words)[256] = calloc(nwords, sizeof(*words));
  if (!words) return 2;
  for (unsigned i = 0; i < nwords; i++)
    if (scanf("%255s", words[i]) != 1) return 3;
  for (unsigned i = 0; i < nkeys; i++) {
    for (unsigned j = 0; j < 6; j++)
      printf("%s%s", j ? " " : "", words[random() % nwords]);
    puts("!");
  }
  for (unsigned i = 0; i < 10000; i++) printf("%llu\n", (unsigned long long)random_u64());
  free(words);
  return 0;
}
