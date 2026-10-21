// Turns Promise.allSettled into "what loaded" plus "what did not".
//
// Pages used to load each section as `api.x().catch(() => [])`, which renders a
// failed request as an empty table: "no schedules", "no licenses", "no
// sessions". An operator cannot tell that from a fleet that genuinely has none.
// With this, a failed section keeps the data it had (undefined on first load)
// and its name is reported so the page can say so.

export type Loaders = Record<string, () => Promise<unknown>>;

export type Settled<T extends Loaders> = {
  data: { [K in keyof T]?: Awaited<ReturnType<T[K]>> };
  failed: (keyof T & string)[];
};

export async function settleAll<T extends Loaders>(loaders: T): Promise<Settled<T>> {
  const keys = Object.keys(loaders) as (keyof T & string)[];
  const results = await Promise.allSettled(keys.map((k) => loaders[k]()));
  const data: Settled<T>['data'] = {};
  const failed: (keyof T & string)[] = [];
  results.forEach((r, i) => {
    const k = keys[i];
    if (r.status === 'fulfilled') {
      data[k] = r.value as Awaited<ReturnType<T[typeof k]>>;
    } else {
      failed.push(k);
    }
  });
  return { data, failed };
}
