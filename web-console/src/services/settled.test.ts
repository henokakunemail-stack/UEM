import { describe, expect, it } from 'vitest';
import { settleAll } from './settled';

describe('settleAll', () => {
  it('reports a failed section instead of turning it into empty data', async () => {
    const { data, failed } = await settleAll({
      schedules: () => Promise.resolve([{ id: 's1' }]),
      runs: () => Promise.reject(new Error('500')),
    });
    expect(data.schedules).toEqual([{ id: 's1' }]);
    expect(data.runs).toBeUndefined();
    expect(failed).toEqual(['runs']);
  });

  it('reports nothing when every section loads', async () => {
    const { failed } = await settleAll({ a: () => Promise.resolve(0), b: () => Promise.resolve([]) });
    expect(failed).toEqual([]);
  });
});
