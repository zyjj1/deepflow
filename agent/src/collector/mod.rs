/*
 * Copyright (c) 2024 Yunshan Networks
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

mod collector;
mod consts;
pub(crate) mod flow_aggr;
pub(crate) mod l7_quadruple_generator;
pub(crate) mod quadruple_generator;
pub(crate) mod types;

use std::net::IpAddr;
use std::thread::JoinHandle;
use std::time::Duration;

pub use collector::{Collector, L7Collector};

use bitflags::bitflags;
use log::info;

use crate::{
    common::endpoint::EPC_INTERNET,
    utils::{possible_host::PossibleHost, stats},
};

use self::l7_quadruple_generator::L7QuadrupleGeneratorThread;
use self::types::{MiniFlow, PeerInfo};
use self::{flow_aggr::FlowAggrThread, quadruple_generator::QuadrupleGeneratorThread};

const SECONDS_IN_MINUTE: u64 = 60;

bitflags! {
    pub struct MetricsType: u32 {
        const SECOND = 1;
        const MINUTE = 1<<1;
   }
}

pub fn round_to_minute(t: Duration) -> Duration {
    Duration::from_secs(t.as_secs() / SECONDS_IN_MINUTE * SECONDS_IN_MINUTE)
}

pub fn check_active(
    now: u64,
    possible_host: &mut Option<PossibleHost>,
    flow: &MiniFlow,
) -> (bool, bool) {
    (
        check_active_host(now, possible_host, &flow.peers[0], &flow.flow_key.ip_src),
        check_active_host(now, possible_host, &flow.peers[1], &flow.flow_key.ip_dst),
    )
}

pub fn reset_delay_seconds(delay_seconds: u64) -> u64 {
    if (SECONDS_IN_MINUTE..SECONDS_IN_MINUTE * 2).contains(&delay_seconds) {
        delay_seconds
    } else if delay_seconds < SECONDS_IN_MINUTE {
        info!(
            "delay_seconds {} < {}, reset delay_seconds to {}.",
            delay_seconds, SECONDS_IN_MINUTE, SECONDS_IN_MINUTE
        );
        SECONDS_IN_MINUTE
    } else {
        info!(
            "delay_seconds {} >= {}, reset delay_seconds to {}.",
            delay_seconds,
            SECONDS_IN_MINUTE * 2,
            SECONDS_IN_MINUTE * 2 - 1
        );
        SECONDS_IN_MINUTE * 2 - 1
    }
}

pub fn check_active_host(
    now: u64,
    possible_host: &mut Option<PossibleHost>,
    flow_metric: &PeerInfo,
    ip: &IpAddr,
) -> bool {
    if flow_metric.is_active_host || flow_metric.l3_epc_id == EPC_INTERNET {
        // 有EPC并且是Device, L3Epc是过平台数据获取的，无需添加到PossibleHost中
        return flow_metric.is_active_host;
    }
    if flow_metric.is_device {
        return true;
    }
    if let Some(possible_host) = possible_host {
        if flow_metric.has_packets {
            // 有EPC无Device的场景是通过CIDR获取的，这里需要加入的PossibleHost中
            possible_host.add(now, ip, flow_metric.l3_epc_id);
            true
        } else {
            possible_host.check(ip, flow_metric.l3_epc_id)
        }
    } else {
        false
    }
}

pub struct CollectorThread {
    pub quadruple_generator: QuadrupleGeneratorThread,
    l4_flow_aggr: Option<FlowAggrThread>,
    second_collector: Option<Collector>,
    minute_collector: Option<Collector>,
}

impl CollectorThread {
    pub fn new(
        quadruple_generator: QuadrupleGeneratorThread,
        l4_flow_aggr: Option<FlowAggrThread>,
        second_collector: Option<Collector>,
        minute_collector: Option<Collector>,
    ) -> Self {
        Self {
            quadruple_generator,
            l4_flow_aggr,
            second_collector,
            minute_collector,
        }
    }

    pub fn start(&mut self) {
        self.quadruple_generator.start();
        if let Some(l4_flow_aggr) = self.l4_flow_aggr.as_mut() {
            l4_flow_aggr.start();
        }
        if let Some(second_collector) = self.second_collector.as_mut() {
            second_collector.start();
        }
        if let Some(minute_collector) = self.minute_collector.as_mut() {
            minute_collector.start();
        }
    }

    pub fn notify_stop(&mut self) -> Vec<JoinHandle<()>> {
        let mut handles = vec![];
        if let Some(h) = self.quadruple_generator.notify_stop() {
            handles.push(h);
        }
        if let Some(h) = self.l4_flow_aggr.as_mut().and_then(|t| t.notify_stop()) {
            handles.push(h);
        }
        if let Some(h) = self.second_collector.as_mut().and_then(|t| t.notify_stop()) {
            handles.push(h);
        }
        if let Some(h) = self.minute_collector.as_mut().and_then(|t| t.notify_stop()) {
            handles.push(h);
        }
        handles
    }

    pub fn stop(&mut self) {
        self.quadruple_generator.stop();
        if let Some(l4_flow_aggr) = self.l4_flow_aggr.as_mut() {
            l4_flow_aggr.stop();
        }
        if let Some(second_collector) = self.second_collector.as_mut() {
            second_collector.stop();
        }
        if let Some(minute_collector) = self.minute_collector.as_mut() {
            minute_collector.stop();
        }
    }
}

pub struct L7CollectorThread {
    pub quadruple_generator: L7QuadrupleGeneratorThread,
    second_collector: Option<L7Collector>,
    minute_collector: Option<L7Collector>,
}

impl L7CollectorThread {
    pub fn new(
        quadruple_generator: L7QuadrupleGeneratorThread,
        second_collector: Option<L7Collector>,
        minute_collector: Option<L7Collector>,
    ) -> Self {
        Self {
            quadruple_generator,
            second_collector,
            minute_collector,
        }
    }

    pub fn start(&mut self) {
        self.quadruple_generator.start();
        if let Some(second_collector) = self.second_collector.as_mut() {
            second_collector.start();
        }
        if let Some(minute_collector) = self.minute_collector.as_mut() {
            minute_collector.start();
        }
    }

    pub fn notify_stop(&mut self) -> Vec<JoinHandle<()>> {
        let mut handles = vec![];
        if let Some(h) = self.quadruple_generator.notify_stop() {
            handles.push(h);
        }
        if let Some(h) = self.second_collector.as_mut().and_then(|t| t.notify_stop()) {
            handles.push(h);
        }
        if let Some(h) = self.minute_collector.as_mut().and_then(|t| t.notify_stop()) {
            handles.push(h);
        }
        handles
    }

    pub fn stop(&mut self) {
        self.quadruple_generator.stop();
        if let Some(second_collector) = self.second_collector.as_mut() {
            second_collector.stop();
        }
        if let Some(minute_collector) = self.minute_collector.as_mut() {
            minute_collector.stop();
        }
    }
}

const FLOW_METRICS_PEER_SRC: usize = 0;
const FLOW_METRICS_PEER_DST: usize = 1;

struct QgStats {
    id: usize,
    kind: &'static str,
}

impl stats::Module for QgStats {
    fn name(&self) -> &'static str {
        "quadruple_generator"
    }

    fn tags(&self) -> Vec<stats::StatsOption> {
        vec![
            stats::StatsOption::Tag("index", self.id.to_string()),
            stats::StatsOption::Tag("kind", self.kind.to_owned()),
        ]
    }
}
