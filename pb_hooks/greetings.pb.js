/// <reference path="../pb_data/types.d.ts" />
onRecordCreateRequest((e) => require(`${__hooks}/greetings.js`).create(e), "greetings");
onRecordUpdate((e) => require(`${__hooks}/greetings.js`).update(e), "greetings");
