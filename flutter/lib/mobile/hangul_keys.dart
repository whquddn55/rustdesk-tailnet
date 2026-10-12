// Turns soft keyboard edits into 2-set (dubeolsik) key presses, so the
// controlled side's own Korean IME composes the text exactly as with a
// physical keyboard: no syllable is deleted and retyped on every jamo.
//
// The controlled side's IME is tracked here: `_composing` holds the keys of
// the syllable it is still composing. Backspace undoes one key of that
// syllable, or a whole character once nothing is composing.
//
// Output (for session_input_ime_keys): ASCII printable characters are typed
// as US keyboard keys, [backspace] and [enter] as those keys, [setKorean] /
// [setEnglish] select the controlled side's input source, and any other
// character is typed as text.

const String setKorean = '\u0001';
const String setEnglish = '\u0002';
const String backspace = '\b';
const String enter = '\n';

const _initialKeys = [
  'r', 'R', 's', 'e', 'E', 'f', 'a', 'q', 'Q', 't', //
  'T', 'd', 'w', 'W', 'c', 'z', 'x', 'v', 'g',
];
const _medialKeys = [
  'k', 'o', 'i', 'O', 'j', 'p', 'u', 'P', 'h', 'hk', //
  'ho', 'hl', 'y', 'n', 'nj', 'np', 'nl', 'b', 'm', 'ml', 'l',
];
const _finalKeys = [
  '', 'r', 'R', 'rt', 's', 'sw', 'sg', 'e', 'f', 'fr', //
  'fa', 'fq', 'ft', 'fx', 'fv', 'fg', 'a', 'q', 'qt', 't',
  'T', 'd', 'w', 'c', 'z', 'x', 'v', 'g',
];
// Hangul compatibility jamo U+3131..U+3163, in code point order.
const _jamoKeys = [
  'r', 'R', 'rt', 's', 'sw', 'sg', 'e', 'E', 'f', 'fr', //
  'fa', 'fq', 'ft', 'fx', 'fv', 'fg', 'a', 'q', 'Q', 'qt',
  't', 'T', 'd', 'w', 'W', 'c', 'z', 'x', 'v', 'g',
  'k', 'o', 'i', 'O', 'j', 'p', 'u', 'P', 'h', 'hk',
  'ho', 'hl', 'y', 'n', 'nj', 'np', 'nl', 'b', 'm', 'ml',
  'l',
];

bool isHangul(int rune) =>
    (rune >= 0xAC00 && rune <= 0xD7A3) || (rune >= 0x3131 && rune <= 0x3163);

/// The 2-set keys that type [rune], or null if it is not Hangul.
List<String>? hangulKeys(int rune) {
  if (rune >= 0xAC00 && rune <= 0xD7A3) {
    final s = rune - 0xAC00;
    return (_initialKeys[s ~/ 588] +
            _medialKeys[(s % 588) ~/ 28] +
            _finalKeys[s % 28])
        .split('');
  }
  if (rune >= 0x3131 && rune <= 0x3163) {
    return _jamoKeys[rune - 0x3131].split('');
  }
  return null;
}

bool _isLatinLetter(int rune) =>
    (rune >= 0x41 && rune <= 0x5A) || (rune >= 0x61 && rune <= 0x7A);

class _Token {
  final String key;
  final bool hangul;
  const _Token(this.key, this.hangul);
}

class HangulKeys {
  List<String> _composing = [];
  bool? _korean;

  /// Forget what the controlled side is composing, e.g. after the text field
  /// was reset or focus may have moved there.
  void reset() {
    _composing = [];
  }

  /// The keys that turn the controlled side's text from [oldValue] into
  /// [newValue].
  String edit(String oldValue, String newValue) {
    final out = StringBuffer();
    var committed = oldValue.runes.toList();
    var composing = _composing;
    if (composing.isNotEmpty) {
      final last = committed.isEmpty ? null : committed.last;
      if (last == null || hangulKeys(last)?.join() != composing.join()) {
        composing = []; // lost track; assume nothing is composing
      } else {
        committed = committed.sublist(0, committed.length - 1);
      }
    }
    final target = newValue.runes.toList();

    var p = 0;
    while (p < committed.length &&
        p < target.length &&
        committed[p] == target[p]) {
      ++p;
    }
    if (p < committed.length) {
      out.write(backspace * (composing.length + committed.length - p));
      composing = [];
      committed = committed.sublist(0, p);
    }

    final rest = target.sublist(committed.length);
    final tokens = <_Token>[];
    for (final r in rest) {
      final keys = hangulKeys(r);
      if (keys != null) {
        tokens.addAll(keys.map((k) => _Token(k, true)));
      } else {
        tokens.add(_Token(String.fromCharCode(r), false));
      }
    }
    var q = 0;
    while (q < composing.length &&
        q < tokens.length &&
        tokens[q].hangul &&
        tokens[q].key == composing[q]) {
      ++q;
    }
    out.write(backspace * (composing.length - q));

    for (final t in tokens.sublist(q)) {
      final rune = t.key.runes.first;
      if (t.hangul && _korean != true) {
        out.write(setKorean);
        _korean = true;
      } else if (!t.hangul && _isLatinLetter(rune) && _korean != false) {
        out.write(setEnglish);
        _korean = false;
      }
      out.write(t.key);
    }

    _composing = rest.isNotEmpty && isHangul(rest.last)
        ? hangulKeys(rest.last)!
        : <String>[];
    return out.toString();
  }
}
