const adminPage: Record<string, string> = { REPEATER: "repeaters", ROOM: "rooms", SENSOR: "sensors" };

// A repeater, room server or sensor is managed on its own admin page; any other peer opens its contact page.
export function contactDetailPath(companion: string, pubkey: string, peerType: string | undefined): string {
  const page = adminPage[(peerType ?? "").toUpperCase()] ?? "contacts";
  return `/companions/${encodeURIComponent(companion)}/${page}/${pubkey}`;
}
