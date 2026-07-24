#include "liveroute/runtime/bounded_queue.hpp"

#include <stdexcept>

namespace {

bool rejects_zero_capacity() {
  try {
    [[maybe_unused]] liveroute::runtime::BoundedQueue<int> queue(0);
  } catch (const std::invalid_argument&) {
    return true;
  }
  return false;
}

}  // namespace

int main() {
  liveroute::runtime::BoundedQueue<int> queue(2);
  if (queue.capacity() != 2 || queue.size() != 0 || !queue.try_push(1) ||
      !queue.try_push(2) || queue.try_push(3) || queue.size() != 2) {
    return 1;
  }

  const auto first = queue.try_pop();
  const auto second = queue.try_pop();
  if (!first.has_value() || !second.has_value() || *first != 1 || *second != 2 ||
      queue.try_pop().has_value() || queue.size() != 0 ||
      !rejects_zero_capacity()) {
    return 1;
  }

  return 0;
}
