import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import HomePage from './pages/HomePage.vue'
import DetailPage from './pages/DetailPage.vue'
import OverviewPage from './pages/OverviewPage.vue'
import './styles.css'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: HomePage },
    { path: '/assessments/:id', name: 'detail', component: DetailPage, props: true },
    { path: '/voyages/:voyage/hatches/latest', name: 'overview', component: OverviewPage, props: true },
  ],
})

createApp(App).use(router).mount('#app')
