#!/usr/bin/env node
// Independent ECMAScript reference for the RFC 8785 conformance comparison.
//
// This script only uses the platform's own serialization (JSON.stringify) and
// the canonicalizer published verbatim in RFC 8785 Appendix A. It implements
// no JCS number, string, or sorting logic of its own, so a shared bug between
// HookFlow and this script is not possible.
//
// Input (stdin), every key optional:
//   {
//     "numbers":   ["<16 lowercase hex digits>", ...],   IEEE 754 binary64 bits
//     "strings":   ["...", ...],                          raw Unicode strings
//     "documents": ["<JSON text>", ...]                   valid I-JSON documents
//   }
// Output (stdout):
//   {
//     "numbers":   ["<JSON.stringify(number)>", ...],
//     "strings":   ["<JSON.stringify(string)>", ...],
//     "documents": ["<Appendix A canonical form>", ...]
//   }
'use strict';

import fs from 'node:fs';

const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const numbers = input.numbers ?? [];
const strings = input.strings ?? [];
const documents = input.documents ?? [];
for (const [key, value] of Object.entries({ numbers, strings, documents })) {
  if (!Array.isArray(value)) {
    process.stderr.write(`v8 conformance: "${key}" must be an array\n`);
    process.exit(2);
  }
}

// RFC 8785 Appendix A, ECMAScript Sample Canonicalizer (verbatim apart from
// the error-free scoping of the accumulator).
const canonicalize = function (object) {
  let buffer = '';
  serialize(object);
  return buffer;

  function serialize(object) {
    if (object === null || typeof object !== 'object' ||
        object.toJSON != null) {
      buffer += JSON.stringify(object);
    } else if (Array.isArray(object)) {
      buffer += '[';
      let next = false;
      object.forEach((element) => {
        if (next) {
          buffer += ',';
        }
        next = true;
        serialize(element);
      });
      buffer += ']';
    } else {
      buffer += '{';
      let next = false;
      Object.keys(object).sort().forEach((property) => {
        if (next) {
          buffer += ',';
        }
        next = true;
        buffer += JSON.stringify(property);
        buffer += ':';
        serialize(object[property]);
      });
      buffer += '}';
    }
  }
};

const out = {
  numbers: numbers.map((hex) => {
    if (typeof hex !== 'string' || !/^[0-9a-f]{16}$/.test(hex)) {
      throw new Error(`invalid IEEE 754 hex value ${JSON.stringify(hex)}`);
    }
    const view = new DataView(new ArrayBuffer(8));
    view.setBigUint64(0, BigInt(`0x${hex}`), false);
    const value = view.getFloat64(0, false);
    if (!Number.isFinite(value)) {
      throw new Error(`non-finite comparison value ${hex}`);
    }
    // RFC 8785 section 3.2.2.3 delegates number serialization to ECMAScript;
    // JSON.stringify is that serialization, including -0 -> "0".
    return JSON.stringify(value);
  }),
  strings: strings.map((value) => {
    if (typeof value !== 'string') {
      throw new Error('string inputs must be strings');
    }
    // RFC 8785 section 3.2.2.2 delegates string serialization to ECMAScript.
    return JSON.stringify(value);
  }),
  documents: documents.map((text) => {
    if (typeof text !== 'string') {
      throw new Error('document inputs must be strings');
    }
    // JSON.parse rounds every number to binary64; the Appendix A canonicalizer
    // then sorts properties with ECMAScript's UTF-16 string comparison.
    return canonicalize(JSON.parse(text));
  }),
};

process.stdout.write(JSON.stringify(out));
