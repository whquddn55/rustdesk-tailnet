// The name this device shows in other users' device lists. Stored in
// `preset-device-name`, which the client sends as the hostname with its system
// info to the API server (hbbs_http/sync.rs), so every account sees it.
import 'package:flutter/material.dart';

import '../../common.dart';
import '../../models/platform_model.dart';

const kOptionPresetDeviceName = 'preset-device-name';

void showDeviceNameDialog({VoidCallback? onChanged}) {
  final controller = TextEditingController(
      text: bind.mainGetOptionSync(key: kOptionPresetDeviceName));
  gFFI.dialogManager.show((setState, close, context) {
    submit() async {
      await bind.mainSetOption(
          key: kOptionPresetDeviceName, value: controller.text.trim());
      close();
      onChanged?.call();
    }

    return CustomAlertDialog(
      title: Text(translate('Device name')),
      content: TextField(
        controller: controller,
        autofocus: true,
        maxLength: 64,
        onSubmitted: (_) => submit(),
      ),
      actions: [
        dialogButton('Cancel', onPressed: close, isOutline: true),
        dialogButton('OK', onPressed: submit),
      ],
      onSubmit: submit,
      onCancel: close,
    );
  });
}
