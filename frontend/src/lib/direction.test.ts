import { describe, expect, it } from 'vitest';
import { cycleDir, normalizeDigits, toArabicDigits } from './direction';

describe('arabic digit helpers', () => {
  it('maps western digits to arabic-indic', () => {
    expect(toArabicDigits('123')).toBe('١٢٣');
    expect(toArabicDigits('a1b2')).toBe('a١b٢');
  });

  it('normalizes arabic-indic and persian digits back', () => {
    expect(normalizeDigits('١٢٣')).toBe('123');
    expect(normalizeDigits('۴۵۶')).toBe('456');
    expect(normalizeDigits('x٧y')).toBe('x7y');
  });

  it('round-trips', () => {
    expect(normalizeDigits(toArabicDigits('9876543210'))).toBe('9876543210');
  });
});

describe('direction cycle', () => {
  it('cycles auto → rtl → ltr → auto', () => {
    expect(cycleDir('auto')).toBe('rtl');
    expect(cycleDir('rtl')).toBe('ltr');
    expect(cycleDir('ltr')).toBe('auto');
  });
});
