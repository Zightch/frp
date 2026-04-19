import ElementPlus from "element-plus";
import { createApp } from "vue";

import "element-plus/dist/index.css";

import { setUnauthorizedHandler } from "./api/http";
import App from "./App.vue";
import { router } from "./router";
import { pinia } from "./stores";
import { useAuthStore } from "./stores/auth";
import "./styles/main.css";

const app = createApp(App);
const authStore = useAuthStore(pinia);

setUnauthorizedHandler(async () => {
  authStore.handleUnauthorized();

  const currentRoute = router.currentRoute.value;
  if (currentRoute.name === "login" || currentRoute.name === "init") {
    return;
  }

  const redirect =
    currentRoute.fullPath && currentRoute.fullPath !== "/login"
      ? { redirect: currentRoute.fullPath }
      : undefined;
  await router.replace({ name: "login", query: redirect });
});

app.use(ElementPlus);
app.use(pinia);
app.use(router);
app.mount("#app");
