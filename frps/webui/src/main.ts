import {
  ElAlert,
  ElButton,
  ElCard,
  ElDescriptions,
  ElDescriptionsItem,
  ElEmpty,
  ElResult,
  ElTag,
  ElTimeline,
  ElTimelineItem,
} from "element-plus";
import { createPinia } from "pinia";
import { createApp } from "vue";

import "element-plus/dist/index.css";

import App from "./App.vue";
import { router } from "./router";
import "./styles/main.css";

const app = createApp(App);

app.use(createPinia());
app.use(router);
[
  ElAlert,
  ElButton,
  ElCard,
  ElDescriptions,
  ElDescriptionsItem,
  ElEmpty,
  ElResult,
  ElTag,
  ElTimeline,
  ElTimelineItem,
].forEach((component) => {
  if (component.name) {
    app.component(component.name, component);
  }
});
app.mount("#app");
