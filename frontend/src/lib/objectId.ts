// Client-side 24-hex ObjectId generation — clients pre-generate element ids
// so creation is optimistic (the server validates shape + uniqueness, §9.4).

let counter = Math.floor(Math.random() * 0xffffff);
const machineId = crypto.getRandomValues(new Uint8Array(5));

export function newObjectId(): string {
  const time = Math.floor(Date.now() / 1000);
  counter = (counter + 1) % 0xffffff;
  const bytes = new Uint8Array(12);
  bytes[0] = (time >> 24) & 0xff;
  bytes[1] = (time >> 16) & 0xff;
  bytes[2] = (time >> 8) & 0xff;
  bytes[3] = time & 0xff;
  bytes.set(machineId, 4);
  bytes[9] = (counter >> 16) & 0xff;
  bytes[10] = (counter >> 8) & 0xff;
  bytes[11] = counter & 0xff;
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

export function newClientId(): string {
  return crypto.randomUUID();
}
