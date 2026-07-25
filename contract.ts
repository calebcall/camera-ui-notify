import { PluginInterface, PluginRole } from '@camera.ui/sdk';

import type { PluginContract } from '@camera.ui/sdk';

export const contract: PluginContract = {
  name: 'Notify',
  role: PluginRole.Hub,
  provides: [],
  consumes: [],
  // Notify is a device-owning notification PUBLISHER'S DELIVERY TARGET: it
  // implements NotifierInterface (getDevices/registerDevice/
  // sendNotification/... — see src/plugin.go) so the host's
  // NotificationManager can dispatch any publisher's notifications to
  // user-registered ntfy/Gotify/webhook targets. Deliberately NOT
  // PluginInterface.NVR (this plugin owns no cameras or recordings) and NOT
  // PluginInterface.OAuthCapable (no authentication flow of any kind — every
  // backend is configured with a plain server URL / token the user supplies
  // directly). Also deliberately no PluginCapability.PublishNotifications:
  // this plugin never calls api.NotificationManager.Publish itself, it only
  // answers the manager's Notifier RPC calls.
  interfaces: [PluginInterface.Notifier],
  capabilities: [],
};

export default contract;
