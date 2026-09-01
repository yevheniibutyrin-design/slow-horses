// Seeds the "rooms" collection with dummy data.
//
// Runs automatically on FIRST boot only: mongo's docker entrypoint executes
// files in /docker-entrypoint-initdb.d when the data directory is empty. A
// later `docker compose up` reuses the volume and skips this file — use
// `docker compose down -v` (or `make reseed`) to start clean.
//
// getSiblingDB is explicit on purpose: without it the script would target
// MONGO_INITDB_DATABASE (defaulting to "test") and the API would read an empty
// collection with no error to explain why.
// The roomUrl values below are baked in for the default host port 5000. If you
// run with API_HOST_PORT set, they still work — POST /join only requires the
// supplied roomUrl to match the stored one — but the links point at 5000.
// MONGO_INITDB_DATABASE is set from MONGO_DB in docker-compose.yml, so the seed
// always lands in the database the API reads.
const dbName = (typeof process !== 'undefined' && process.env && process.env.MONGO_INITDB_DATABASE) || 'slowhorses';
db = db.getSiblingDB(dbName);

if (!db.getCollectionNames().includes('rooms')) {
  db.createCollection('rooms');
}

const rooms = [
  {
    _id: '11111111-1111-4111-8111-111111111111',
    name: 'Friday Night Table',
    participants: [
      { name: 'Alice', balance: { usd: 250, chips: 50 } },
      { name: 'Bob', balance: { usd: 100 } },
    ],
    isFilled: false,
    isStarted: false,
    eventId: 'evt-2026-09-04',
    roomUrl: 'http://localhost:5000/join/11111111-1111-4111-8111-111111111111',
  },
  {
    _id: '22222222-2222-4222-8222-222222222222',
    name: 'Champions League Pool',
    participants: [
      // balance is Object<any>: shapes deliberately differ between participants.
      { name: 'Carol', balance: { eur: 75.5, freeBets: { count: 2, currency: 'EUR' } } },
    ],
    isFilled: false,
    isStarted: true,
    eventId: 'evt-ucl-2026-09-15',
    roomUrl: 'http://localhost:5000/join/22222222-2222-4222-8222-222222222222',
  },
  {
    _id: '33333333-3333-4333-8333-333333333333',
    name: 'Sold Out Derby Room',
    participants: [
      { name: 'Dave', balance: { gbp: 20 } },
      { name: 'Erin', balance: {} },
    ],
    isFilled: true,
    isStarted: false,
    eventId: 'evt-derby-2026-10-01',
    roomUrl: 'http://localhost:5000/join/33333333-3333-4333-8333-333333333333',
  },
];

// Guarded so re-running the script by hand through mongosh is harmless.
if (db.rooms.countDocuments() === 0) {
  db.rooms.insertMany(rooms);
  print('seeded ' + rooms.length + ' rooms into ' + dbName + '.rooms');
} else {
  print('rooms already present (' + db.rooms.countDocuments() + ' docs), skipping seed');
}
