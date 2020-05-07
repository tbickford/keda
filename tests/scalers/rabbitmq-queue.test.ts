import * as async from 'async'
import * as fs from 'fs'
import * as sh from 'shelljs'
import * as tmp from 'tmp'
import * as amqp from 'amqplib/callback_api'
import * as child_process from 'child_process'
import test from 'ava'

const defaultNamespace = 'rabbitmq-queue-test'
// let localConnectionString = '' // TODO: Replace this: process.env['TEST_STORAGE_CONNECTION_STRING']
const rabbitmqNamespace = 'default'
const queueName = 'hello'
const username = "test-user"
const password = "test-password"
const vhost = "test-vm"
const clusterLocalConnectionString = `amqp://${username}:${password}@rabbitmq.default.svc.cluster.local/${vhost}`

test.before(t => {
  if (!clusterLocalConnectionString) {
    //  t.fail('TEST_STORAGE_CONNECTION_STRING environment variable is required for queue tests')
  }
  // create rabbitmq deployment
  sh.exec(`kubectl apply -n ${rabbitmqNamespace} -f scalers/rabbitmq-deployment.yaml`)
  // wait for rabbitmq  // TODO: put better logic here
  for (let i = 0; i < 10; i++) {
    const readyReplicaCount = sh.exec(
      `kubectl get deploy/rabbitmq -n ${rabbitmqNamespace} -o jsonpath='{.status.readyReplicas}'`).stdout
    if (readyReplicaCount != '2') {
      sh.exec('sleep 2s')
    }
  }

  sh.config.silent = true
  const base64ConStr = Buffer.from(clusterLocalConnectionString).toString('base64')
  const tmpFile = tmp.fileSync()
  fs.writeFileSync(tmpFile.name, deployYaml.replace('{{CONNECTION_STRING_BASE64}}', base64ConStr)
    .replace('{{CONNECTION_STRING}}', clusterLocalConnectionString)
    .replace('{{QUEUE_NAME}}', queueName))
  sh.exec(`kubectl create namespace ${defaultNamespace}`)
  t.is(
    0,
    sh.exec(`kubectl apply -f ${tmpFile.name} --namespace ${defaultNamespace}`).code,
    'creating a deployment should work.'
  )
})

test.serial('Deployment should have 0 replicas on start', t => {
  const replicaCount = sh.exec(
    `kubectl get deployment.apps/test-deployment --namespace ${defaultNamespace} -o jsonpath="{.spec.replicas}"`
  ).stdout
  t.is(replicaCount, '0', 'replica count should start out as 0')
})

test.serial.cb(
  'Deployment should scale to 4 with 500 messages on the queue then back to 0',
  t => {
    const localConnectionString = `amqp://${username}:${password}@localhost:5672/${vhost}`
    const spawn = child_process.spawn("kubectl", ["port-forward", "-n", rabbitmqNamespace, "svc/rabbitmq", "5672:5672"])
    sh.exec("sleep 2s")

    try {
      // add 500 messages    
      amqp.connect(localConnectionString,
        (err, connection) => {
          t.falsy(err, 'unable to create connection')
          connection.createConfirmChannel((err2, channel) => {
            t.falsy(err2, 'unable to create channel')
            channel.assertQueue(queueName, { durable: false })
            for (let n = 1; n <= 500; n++) {
              channel.sendToQueue(queueName, Buffer.from(`test ${n}`))
            }
            channel.waitForConfirms((err) => {
              t.falsy(err, 'Waiting for channel errors')
              //sh.exec('sleep 30s')
              let replicaCount = '0'
              for (let i = 0; i < 10 && replicaCount !== '4'; i++) {
                replicaCount = sh.exec(
                  `kubectl get deployment.apps/test-deployment --namespace ${defaultNamespace} -o jsonpath="{.spec.replicas}"`
                ).stdout
                t.log('replica count is:' + replicaCount)
                if (replicaCount !== '4') {
                  sh.exec('sleep 5s')
                }
              }

              t.is('4', replicaCount, 'Replica count should be 4 after 10 seconds')

              for (let i = 0; i < 50 && replicaCount !== '0'; i++) {
                replicaCount = sh.exec(
                  `kubectl get deployment.apps/test-deployment --namespace ${defaultNamespace} -o jsonpath="{.spec.replicas}"`
                ).stdout
                if (replicaCount !== '0') {
                  sh.exec('sleep 5s')
                }
              }

              t.is('0', replicaCount, 'Replica count should be 0 after 3 minutes')
              connection.close()
              spawn.kill('SIGHUP')
              t.end()
            })
          })
        })
    } catch (err) {
      spawn.kill('SIGHUP');
      throw err
    }
  }
)


test.after.always.cb('clean up rabbitmq-queue deployment', t => {
  const resources = [
    'secret/test-secrets',
    'deployment.apps/test-deployment',
    'scaledobject.keda.k8s.io/test-scaledobject',
  ]

  for (const resource of resources) {
    sh.exec(`kubectl delete ${resource} --namespace ${defaultNamespace}`)
  }
  sh.exec(`kubectl delete namespace ${defaultNamespace}`)

  // remove rabbitmq 
  sh.exec(`kubectl delete -n ${rabbitmqNamespace} -f scalers/rabbitmq-deployment.yaml`)
  t.end()
})


const deployYaml = `apiVersion: v1
kind: Secret
metadata:
  name: test-secrets
data:
  RabbitMqHost: {{CONNECTION_STRING_BASE64}}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deployment
  labels:
    app: test-deployment
spec:
  replicas: 0 
  selector:
    matchLabels:
      app: test-deployment
  template:
    metadata:
      labels:
        app: test-deployment
    spec:
      containers:
      - name: rabbitmq-consumer
        image: jeffhollan/rabbitmq-client:dev
        imagePullPolicy: Always
        command:
          - receive
        args:
          - '{{CONNECTION_STRING}}'
        envFrom:
        - secretRef:
            name: test-secrets
---
apiVersion: keda.k8s.io/v1alpha1
kind: ScaledObject
metadata:
  name: test-scaledobject
  labels:
    deploymentName: test-deployment
spec:
  scaleTargetRef:
    deploymentName: test-deployment
  pollingInterval: 5
  cooldownPeriod: 10
  minReplicaCount: 0
  maxReplicaCount: 4
  triggers:
  - type: rabbitmq
    metadata:
      queueName: {{QUEUE_NAME}}
      host: RabbitMqHost
      queueLength  : '50'`